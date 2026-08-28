package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	agentutils "github.com/xichan96/cortex/agent/utils"
)

// errTool is a MockTool variant whose Execute always returns the configured error.
type errTool struct {
	MockTool
	err error
}

func (e *errTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return nil, e.err
}

// TestNonFatal_FatalPassthrough verifies every fatal error class (FatalToolError,
// ApprovalRejectedError, LoopDetectedError) is passed through as a real error
// instead of being fed back to the model as {ok:false}. (F3/P4.2)
func TestNonFatal_FatalPassthrough(t *testing.T) {
	fatals := []error{
		&types.FatalToolError{Err: errors.New("bad input"), Reason: "validation"},
		&ApprovalRejectedError{ToolName: "bash"},
		&LoopDetectedError{ToolName: "bash", Suggestion: "change strategy"},
		// A fatal error buried under a %w wrap must still be caught.
		fmt.Errorf("outer: %w", &types.FatalToolError{Reason: "wrapped"}),
	}
	for _, fe := range fatals {
		tool := WrapNonFatalTool(&errTool{MockTool: MockTool{name: "t"}, err: fe})
		res, err := tool.Execute(context.Background(), nil)
		if err == nil {
			t.Fatalf("fatal error %v should be passed through, got result %v", fe, res)
		}
		if !types.IsFatalToolError(err) {
			t.Fatalf("expected passthrough to preserve fatal classification for %v, got %T", fe, err)
		}
	}
}

// TestNonFatal_RecoverableFeedsBack verifies recoverable errors are converted to
// {ok:false} results fed back to the model.
func TestNonFatal_RecoverableFeedsBack(t *testing.T) {
	recoverable := []error{
		errors.New("MCP call failed: connection refused"),
		errors.New("tool execution timeout"),
		errors.New("file not found"),
	}
	for _, e := range recoverable {
		tool := WrapNonFatalTool(&errTool{MockTool: MockTool{name: "t"}, err: e})
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("recoverable error %v should feed back as result, got err %v", e, err)
		}
		m, ok := res.(map[string]interface{})
		if !ok || m["ok"] != false {
			t.Fatalf("expected {ok:false} result for %v, got %v", e, res)
		}
	}
}

// TestNonFatal_CtxCancelPassesThrough verifies a cancelled ctx still surfaces as
// a real error (not swallowed into {ok:false}).
func TestNonFatal_CtxCancelPassesThrough(t *testing.T) {
	tool := WrapNonFatalTool(&errTool{MockTool: MockTool{name: "t"}, err: errors.New("boom")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, nil)
	if err == nil {
		t.Fatal("cancelled ctx should surface as a real error")
	}
}

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

type MockDetector struct {
	actions []agentutils.LoopDetectAction
}

func (d *MockDetector) Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult {
	return &agentutils.LoopDetectResult{IsLoop: false}
}
func (d *MockDetector) Record(sessionID string, action agentutils.LoopDetectAction) {
	d.actions = append(d.actions, action)
}
func (d *MockDetector) RecordWithResult(sessionID string, action agentutils.LoopDetectAction, resultHash string) {
	d.actions = append(d.actions, action)
}
func (d *MockDetector) Reset(sessionID string) {}
func (d *MockDetector) GetStats(sessionID string) agentutils.LoopDetectStats {
	return agentutils.LoopDetectStats{}
}

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
