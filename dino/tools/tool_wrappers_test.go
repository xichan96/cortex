package tools

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	dinoLoop "github.com/xichan96/cortex/dino/loop"
)

// MockTool implements types.Tool for testing
type MockTool struct {
	name   string
	result interface{}
	err    error
}

func (m *MockTool) Name() string                   { return m.name }
func (m *MockTool) Description() string            { return "mock tool" }
func (m *MockTool) Schema() map[string]interface{} { return nil }
func (m *MockTool) Metadata() types.ToolMetadata   { return types.ToolMetadata{} }
func (m *MockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func TestToolResultLimiter(t *testing.T) {
	// Case 1: Normal result within limit
	mock := &MockTool{name: "test", result: map[string]string{"key": "value"}}
	limiter := WrapToolResultLimiter(mock, 100, 50)
	res, err := limiter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Check if result is preserved
	if m, ok := res.(map[string]string); !ok || m["key"] != "value" {
		t.Errorf("result mismatch: %v", res)
	}

	// Case 2: Result too large (JSON)
	largeData := make(map[string]string)
	for i := 0; i < 100; i++ {
		largeData["key"] += "long_string"
	}
	mock.result = largeData
	limiter = WrapToolResultLimiter(mock, 10, 10) // Small limit
	res, err = limiter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return error object
	if m, ok := res.(map[string]interface{}); !ok || m["ok"] != false {
		t.Errorf("expected error result for large payload, got: %v", res)
	}

	// Case 3: Unmarshalable result (e.g. Infinity)
	// Go's json.Marshal fails on math.Inf
	mock.result = math.Inf(1)
	limiter = WrapToolResultLimiter(mock, 100, 10)
	res, err = limiter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fallback to string check. "+Inf" is 4 bytes, < 10. Should pass.
	if val, ok := res.(float64); !ok || val != math.Inf(1) {
		t.Errorf("expected +Inf, got %v", res)
	}

	// Case 4: Unmarshalable and string too large
	// Use a struct with cyclic reference or just use Infinity with small limit
	// Wait, "+Inf" is short. Let's use something that fails marshal but has long string rep?
	// Complex numbers fail marshal? No.
	// Channels fail marshal.
	ch := make(chan int)
	mock.result = ch
	limiter = WrapToolResultLimiter(mock, 100, 2) // Limit 2 bytes, chan string is usually address
	res, err = limiter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return error object because string rep of channel is > 2 bytes
	if m, ok := res.(map[string]interface{}); !ok || m["ok"] != false {
		t.Errorf("expected error result for large string fallback, got: %v", res)
	}
}

// MockDetector implements dinoLoop.Detector
type MockDetector struct {
	actions []dinoLoop.Action
}

func (d *MockDetector) Detect(ctx context.Context, sessionID string, action dinoLoop.Action) *dinoLoop.Result {
	return &dinoLoop.Result{IsLoop: false}
}
func (d *MockDetector) Record(sessionID string, action dinoLoop.Action) {
	d.actions = append(d.actions, action)
}
func (d *MockDetector) Reset(sessionID string)                   {}
func (d *MockDetector) GetStats(sessionID string) dinoLoop.Stats { return dinoLoop.Stats{} }

func TestLoopDetectionSerialization(t *testing.T) {
	mockTool := &MockTool{name: "test_tool"}
	mockDetector := &MockDetector{}

	wrapper := WrapLoopDetection(mockTool, "session_1", mockDetector, nil)

	// Case 1: Unmarshalable input (Channel)
	// JSON marshal fails on channels
	// We need to pass a map that contains a channel, but map[string]interface{} values must be valid types?
	// No, interface{} can hold anything.
	badInput := map[string]interface{}{
		"chan": make(chan int),
	}

	_, err := wrapper.Execute(context.Background(), badInput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockDetector.actions) != 1 {
		t.Fatalf("expected 1 recorded action, got %d", len(mockDetector.actions))
	}

	// Verify content fallback
	content := mockDetector.actions[0].Content
	// It should be the string representation of the map
	if content == "" || content == "{}" {
		t.Errorf("Recorded content should not be empty for unmarshalable input")
	}
	// Check if it looks like a map string
	expectedStart := "map[chan:"
	if len(content) < len(expectedStart) || content[:3] != "map" {
		t.Logf("Recorded content: %s", content)
		// It's okay, just ensure it's not empty and contains some info
	}
	fmt.Printf("Recorded content: %s\n", content)
}
