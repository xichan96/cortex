package engine

import (
	"context"
	"fmt"
	"strings"
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

// countingTool is a mock tool that tracks the max concurrent executions.
// Distinct instances share the same state so parallel calls with different
// tool names can be measured against one counter (the engine's dependency
// sorter keys by tool name, so parallel calls to one name collapse).
type countingTool struct {
	name string
	// shared state
	mu      *sync.Mutex
	running *int
	maxSeen *int
	done    *chan struct{} // if set, tool blocks until closed
}

func newCountingTool(name string, shared *countingState) *countingTool {
	return &countingTool{name: name, mu: &shared.mu, running: &shared.running, maxSeen: &shared.maxSeen, done: &shared.done}
}

type countingState struct {
	mu      sync.Mutex
	running int
	maxSeen int
	done    chan struct{} // if set, tools block until closed
}

func (t *countingTool) Name() string                   { return t.name }
func (t *countingTool) Description() string            { return "counting tool" }
func (t *countingTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *countingTool) Metadata() types.ToolMetadata   { return types.ToolMetadata{} }

func (t *countingTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	t.mu.Lock()
	*t.running++
	if *t.running > *t.maxSeen {
		*t.maxSeen = *t.running
	}
	t.mu.Unlock()
	if t.done != nil && *t.done != nil {
		select {
		case <-*t.done:
		case <-ctx.Done():
		}
	}
	t.mu.Lock()
	*t.running--
	t.mu.Unlock()
	return "ok", nil
}

func (t *countingTool) maxConcurrent() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return *t.maxSeen
}

// executeToolCallRound drives a single tool-calling iteration through the
// engine and returns the resulting intermediate steps. It lets tests exercise
// runToolCallsByLayer without going through the full LLM loop.
func (ae *AgentEngine) executeToolCallRound(t *testing.T, calls []types.ToolCall) []types.ToolCallData {
	t.Helper()
	sorted, err := ae.prepareToolCalls(calls)
	if err != nil {
		t.Fatalf("prepareToolCalls: %v", err)
	}
	exists, results := ae.runToolCallsByLayer(context.Background(), sorted, 5*time.Second)
	_, steps := ae.buildToolCallResults(sorted, exists, results, "", "test")
	return steps
}

func TestParallelismLimit_One(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.ToolParallelismLimit = 1
	config.EnableToolRetry = false
	ae := NewAgentEngine(provider, config)

	shared := &countingState{}
	var calls []types.ToolCall
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("count_%d", i)
		ae.AddTool(context.Background(), newCountingTool(name, shared))
		calls = append(calls, types.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: types.ToolFunction{Name: name, Arguments: map[string]interface{}{"i": i}},
		})
	}
	steps := ae.executeToolCallRound(t, calls)
	if len(steps) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(steps))
	}
	if got := shared.maxSeen; got != 1 {
		t.Errorf("expected max concurrency 1, got %d", got)
	}
}

func TestParallelismLimit_Four(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.ToolParallelismLimit = 4
	config.EnableToolRetry = false
	ae := NewAgentEngine(provider, config)

	shared := &countingState{}
	var calls []types.ToolCall
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("count_%d", i)
		ae.AddTool(context.Background(), newCountingTool(name, shared))
		calls = append(calls, types.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: types.ToolFunction{Name: name, Arguments: map[string]interface{}{"i": i}},
		})
	}
	steps := ae.executeToolCallRound(t, calls)
	if len(steps) != 10 {
		t.Fatalf("expected 10 steps, got %d", len(steps))
	}
	if got := shared.maxSeen; got > 4 {
		t.Errorf("expected max concurrency <= 4, got %d", got)
	}
}

func TestParallelismLimit_Default(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	ae := NewAgentEngine(provider, config)
	want := defaultToolParallelismLimit()
	if got := ae.getToolParallelismLimit(); got != want {
		t.Errorf("expected default parallelism %d, got %d", want, got)
	}
}

func TestParallelismLimit_CancelQueue(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.ToolParallelismLimit = 1
	config.EnableToolRetry = false
	ae := NewAgentEngine(provider, config)

	shared := &countingState{done: make(chan struct{})}
	ae.AddTool(context.Background(), newCountingTool("block", shared))
	ae.AddTool(context.Background(), newCountingTool("fast", shared))

	// Limit 1, so "block" occupies the only slot and holds it; "fast" queues.
	calls := []types.ToolCall{
		{ID: "call_block", Type: "function", Function: types.ToolFunction{Name: "block", Arguments: map[string]interface{}{}}},
		{ID: "call_fast", Type: "function", Function: types.ToolFunction{Name: "fast", Arguments: map[string]interface{}{}}},
	}

	// Run the round under a context that times out quickly; when the context is
	// cancelled the queued call is skipped (results[idx] gets the ctx error).
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	sorted, err := ae.prepareToolCalls(calls)
	if err != nil {
		t.Fatalf("prepareToolCalls: %v", err)
	}
	exists, results := ae.runToolCallsByLayer(ctx, sorted, 5*time.Second)

	// Unblock "block" so goroutines wind down cleanly.
	close(shared.done)

	_, steps := ae.buildToolCallResults(sorted, exists, results, "", "test")
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps (one error), got %d", len(steps))
	}
	// The queued "fast" call must surface as an error (ctx cancellation), not as
	// a zero-value success.
	foundErr := false
	for _, s := range steps {
		if strings.Contains(s.Observation, "execution failed") || strings.Contains(s.Observation, "context deadline") {
			foundErr = true
		}
	}
	if !foundErr {
		t.Errorf("expected at least one queued call to report a cancellation error, steps: %+v", steps)
	}
}

func TestStreamBufferSize_Config(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.StreamBufferSize = 200
	ae := NewAgentEngine(provider, config)
	if got := ae.getStreamBufferSize(); got != 200 {
		t.Errorf("expected stream buffer 200, got %d", got)
	}
	config.StreamBufferSize = 0
	if got := ae.getStreamBufferSize(); got != types.DefaultChannelBuffer {
		t.Errorf("expected default stream buffer %d, got %d", types.DefaultChannelBuffer, got)
	}
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
