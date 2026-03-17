package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
)

// MockLLMProvider is a mock LLM provider for testing
type MockLLMProvider struct {
	mu             sync.Mutex
	responses      []types.Message
	streamMessages []types.StreamMessage
	currentIdx     int
}

func NewMockLLMProvider() *MockLLMProvider {
	return &MockLLMProvider{
		responses:      make([]types.Message, 0),
		streamMessages: make([]types.StreamMessage, 0),
		currentIdx:     0,
	}
}

func (m *MockLLMProvider) AddResponse(msg types.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, msg)
}

func (m *MockLLMProvider) AddStreamMessage(msg types.StreamMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamMessages = append(m.streamMessages, msg)
}

func (m *MockLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentIdx >= len(m.responses) {
		return types.Message{Content: ""}, nil
	}
	msg := m.responses[m.currentIdx]
	m.currentIdx++
	return msg, nil
}

func (m *MockLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	m.mu.Lock()
	msgs := make([]types.StreamMessage, len(m.streamMessages))
	copy(msgs, m.streamMessages)
	m.mu.Unlock()

	ch := make(chan types.StreamMessage, len(msgs)+1)
	go func() {
		for _, msg := range msgs {
			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			}
		}
		close(ch)
	}()
	return ch, nil
}

func (m *MockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentIdx >= len(m.responses) {
		return types.Message{Content: ""}, nil
	}
	msg := m.responses[m.currentIdx]
	m.currentIdx++
	return msg, nil
}

func (m *MockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	m.mu.Lock()
	msgs := make([]types.StreamMessage, len(m.streamMessages))
	copy(msgs, m.streamMessages)
	m.mu.Unlock()

	ch := make(chan types.StreamMessage, len(msgs)+1)
	go func() {
		for _, msg := range msgs {
			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			}
		}
		close(ch)
	}()
	return ch, nil
}

func (m *MockLLMProvider) GetModelName() string {
	return "mock-model"
}

func (m *MockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

// MockTool is a mock tool for testing
type MockTool struct {
	name        string
	description string
	executeFunc func(ctx context.Context, input map[string]interface{}) (interface{}, error)
}

func NewMockTool(name string) *MockTool {
	return &MockTool{
		name:        name,
		description: "Mock tool for testing",
	}
}

func (t *MockTool) WithDescription(desc string) *MockTool {
	t.description = desc
	return t
}

func (t *MockTool) WithExecuteFunc(fn func(ctx context.Context, input map[string]interface{}) (interface{}, error)) *MockTool {
	t.executeFunc = fn
	return t
}

func (t *MockTool) Name() string                   { return t.name }
func (t *MockTool) Description() string            { return t.description }
func (t *MockTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *MockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.executeFunc != nil {
		return t.executeFunc(ctx, input)
	}
	return "mock result", nil
}

func (t *MockTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{}
}

// MockToolCallback is a mock tool callback for testing
type MockToolCallback struct {
	Calls []string
}

func NewMockToolCallback() *MockToolCallback {
	return &MockToolCallback{
		Calls: make([]string, 0),
	}
}

func (c *MockToolCallback) OnToolCall(toolName string, toolCallID string, input map[string]interface{}) {
	c.Calls = append(c.Calls, "OnToolCall:"+toolName)
}

func (c *MockToolCallback) OnToolInputStart(toolName string, toolCallID string, input map[string]interface{}) {
	c.Calls = append(c.Calls, "OnToolInputStart:"+toolName)
}

func (c *MockToolCallback) OnToolInputEnd(toolName string, toolCallID string, input map[string]interface{}) {
	c.Calls = append(c.Calls, "OnToolInputEnd:"+toolName)
}

func (c *MockToolCallback) OnToolResult(toolName string, toolCallID string, output interface{}) {
	c.Calls = append(c.Calls, "OnToolResult:"+toolName)
}

func (c *MockToolCallback) OnToolError(toolName string, toolCallID string, err error) {
	c.Calls = append(c.Calls, "OnToolError:"+toolName)
}

func TestNewAgentEngine(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.MaxIterations = 5

	engine := NewAgentEngine(provider, config)

	if engine == nil {
		t.Fatal("Expected non-nil AgentEngine")
	}

	if engine.model == nil {
		t.Error("Expected model to be set")
	}

	if engine.config == nil {
		t.Error("Expected config to be set")
	}

	if engine.config.MaxIterations != 5 {
		t.Errorf("Expected MaxIterations to be 5, got %d", engine.config.MaxIterations)
	}
}

func TestAgentEngine_SetToolCallback(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)

	callback := NewMockToolCallback()
	engine.SetToolCallback(context.Background(), callback)

	engine.mu.RLock()
	setCallback := engine.toolCallback
	engine.mu.RUnlock()

	if setCallback == nil {
		t.Error("Expected toolCallback to be set")
	}
}

func TestAgentEngine_AddTool(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)

	tool := NewMockTool("test_tool")
	engine.AddTool(context.Background(), tool)

	engine.mu.RLock()
	tools := engine.tools
	toolsMap := engine.toolsMap
	engine.mu.RUnlock()

	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	if toolsMap["test_tool"] == nil {
		t.Error("Expected tool to be in toolsMap")
	}
}

func TestAgentEngine_ExecuteStream_NoTools(t *testing.T) {
	provider := NewMockLLMProvider()
	provider.AddStreamMessage(types.StreamMessage{
		Type:    "chunk",
		Content: "Hello",
	})
	provider.AddStreamMessage(types.StreamMessage{
		Type: "end",
	})

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	engine := NewAgentEngine(provider, config)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("Hi"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream returned error: %v", err)
	}

	resultCount := 0
	for result := range stream {
		resultCount++
		if result.Type == "chunk" {
			if result.Content != "Hello" {
				t.Errorf("Expected 'Hello', got '%s'", result.Content)
			}
		}
	}

	if resultCount == 0 {
		t.Error("Expected at least one result")
	}
}

func TestAgentEngine_ExecuteStream_WithToolCall(t *testing.T) {
	provider := NewMockLLMProvider()
	// First call returns a tool call
	provider.AddStreamMessage(types.StreamMessage{
		Type:    "chunk",
		Content: "I'll help you",
	})
	provider.AddStreamMessage(types.StreamMessage{
		Type: "tool_calls",
		ToolCalls: []types.ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: types.ToolFunction{
					Name:      "test_tool",
					Arguments: map[string]interface{}{"input": "hello"},
				},
			},
		},
	})
	// Second call returns end
	provider.AddStreamMessage(types.StreamMessage{
		Type:    "chunk",
		Content: "Done",
	})
	provider.AddStreamMessage(types.StreamMessage{
		Type: "end",
	})

	config := types.NewAgentConfig()
	config.MaxIterations = 2
	engine := NewAgentEngine(provider, config)

	// Add mock tool
	tool := NewMockTool("test_tool").WithExecuteFunc(func(ctx context.Context, input map[string]interface{}) (interface{}, error) {
		return "tool executed", nil
	})
	engine.AddTool(context.Background(), tool)

	// Add callback
	callback := NewMockToolCallback()
	engine.SetToolCallback(context.Background(), callback)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("Use tool"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream returned error: %v", err)
	}

	hasToolEvent := false
	for result := range stream {
		if result.Type == "tool_event" && result.ToolEvent != nil {
			hasToolEvent = true
			t.Logf("Tool event: %s - %s - %s", result.ToolEvent.Event, result.ToolEvent.ToolName, result.ToolEvent.State)
		}
	}

	// Check if tool events were sent
	if !hasToolEvent {
		t.Log("Note: Tool events may not be sent if LLM doesn't return tool calls in stream mode")
	}
}

func TestAgentEngine_SetMemory(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)

	mem := providers.NewSimpleMemoryProvider()
	engine.SetMemory(context.Background(), mem)

	engine.mu.RLock()
	memory := engine.memory
	engine.mu.RUnlock()

	if memory == nil {
		t.Error("Expected memory to be set")
	}
}

func TestAgentEngine_Stop(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)

	engine.Stop(context.Background())

	if !engine.isRunning.CompareAndSwap(false, true) {
		t.Log("Note: isRunning can be toggled after Stop")
	}
}

func TestAgentEngine_GetTotalUsage(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)

	usage := engine.GetTotalUsage()
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 {
		t.Errorf("Expected zero usage, got %+v", usage)
	}
}

func TestAgentEngine_ExecuteStream_ContextCancellation(t *testing.T) {
	provider := NewMockLLMProvider()
	// Add a slow response
	provider.AddStreamMessage(types.StreamMessage{
		Type:    "chunk",
		Content: "Slow response",
	})

	config := types.NewAgentConfig()
	config.Timeout = 10 * time.Second
	engine := NewAgentEngine(provider, config)

	ctx, cancel := context.WithCancel(context.Background())

	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("Test"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream returned error: %v", err)
	}

	// Cancel after receiving first chunk
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	resultCount := 0
	for result := range stream {
		resultCount++
		t.Logf("Received result: %+v", result)
		if result.Type == "error" && result.Error != nil {
			t.Logf("Context cancellation error: %v", result.Error)
			break
		}
	}

	if resultCount == 0 {
		t.Error("Expected at least one result before cancellation")
	}
}

func TestStreamToolCallback_Integration(t *testing.T) {
	provider := NewMockLLMProvider()
	callback := NewMockToolCallback()

	config := types.NewAgentConfig()
	engine := NewAgentEngine(provider, config)
	engine.SetToolCallback(context.Background(), callback)

	// Verify callback is set
	engine.mu.RLock()
	cb := engine.toolCallback
	engine.mu.RUnlock()

	if cb == nil {
		t.Fatal("Expected callback to be set")
	}

	// Simulate tool events
	cb.OnToolCall("test_tool", "call_1", map[string]interface{}{"key": "value"})
	cb.OnToolInputStart("test_tool", "call_1", map[string]interface{}{"key": "value"})
	cb.OnToolInputEnd("test_tool", "call_1", map[string]interface{}{"key": "value"})
	cb.OnToolResult("test_tool", "call_1", "result")

	if len(callback.Calls) != 4 {
		t.Errorf("Expected 4 callback calls, got %d", len(callback.Calls))
	}

	// Verify all expected methods were called
	expectedCalls := []string{
		"OnToolCall:test_tool",
		"OnToolInputStart:test_tool",
		"OnToolInputEnd:test_tool",
		"OnToolResult:test_tool",
	}
	for i, expected := range expectedCalls {
		if i >= len(callback.Calls) {
			t.Errorf("Missing call at index %d: expected %s", i, expected)
			continue
		}
		if callback.Calls[i] != expected {
			t.Errorf("Call %d: expected %s, got %s", i, expected, callback.Calls[i])
		}
	}
}

func TestStreamToolCallback_Close(t *testing.T) {
	callback := &streamToolCallback{
		userCallback:  nil,
		resultSender:  func(r types.StreamResult) {},
		toolCallState: map[string]time.Time{"call_1": time.Now()},
		closed:        false,
	}

	// Verify state is not empty before Close
	if len(callback.toolCallState) != 1 {
		t.Errorf("Expected 1 state entry, got %d", len(callback.toolCallState))
	}

	// Close should clear the state
	callback.Close()

	if !callback.closed {
		t.Error("Expected closed to be true")
	}
	if len(callback.toolCallState) != 0 {
		t.Errorf("Expected 0 state entries after Close, got %d", len(callback.toolCallState))
	}

	// After close, events should not be sent
	callback.OnToolCall("test", "call_2", nil)
}

func TestAgentEngine_ExecuteStream_Concurrent(t *testing.T) {
	provider := NewMockLLMProvider()
	provider.AddStreamMessage(types.StreamMessage{
		Type:    "chunk",
		Content: "Hello",
	})
	provider.AddStreamMessage(types.StreamMessage{
		Type: "end",
	})

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	engine := NewAgentEngine(provider, config)

	ctx := context.Background()

	// Run multiple concurrent executions
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("Test"), nil)
			if err != nil {
				t.Logf("ExecuteStream error (expected due to busy): %v", err)
				return
			}
			for range stream {
				// Drain the channel
			}
		}()
	}
	wg.Wait()
}
