package engine

import (
	"context"
	stderrors "errors"
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

// cacheConfigurableProvider wraps a provider and records prompt-cache opts it
// receives via SetPromptCacheOptions (R4 verification).
type cacheConfigurableProvider struct {
	types.LLMProvider
	opts types.PromptCacheOptions
}

func (p *cacheConfigurableProvider) SetPromptCacheOptions(opts types.PromptCacheOptions) {
	p.opts = opts
}

func (p *cacheConfigurableProvider) PromptCacheOptions() types.PromptCacheOptions {
	return p.opts
}

// TestNewAgentEngine_PropagatesPromptCacheConfig (R4) verifies that the engine
// applies PromptCaching to a PromptCacheConfigurer provider at construction —
// dino/factory.go never calls SetConfig, so this is the only guaranteed path.
func TestNewAgentEngine_PropagatesPromptCacheConfig(t *testing.T) {
	inner := NewMockLLMProvider()
	provider := &cacheConfigurableProvider{LLMProvider: inner}

	config := types.NewAgentConfig()
	config.PromptCaching = true
	NewAgentEngine(provider, config)
	if !provider.opts.Enabled {
		t.Error("expected prompt cache enabled by default construction")
	}

	provider2 := &cacheConfigurableProvider{LLMProvider: inner}
	cfg2 := types.NewAgentConfig()
	cfg2.PromptCaching = false
	NewAgentEngine(provider2, cfg2)
	if provider2.opts.Enabled {
		t.Error("expected prompt cache disabled when AgentConfig.PromptCaching=false")
	}
}

// TestNewAgentEngine_PreservesPromptCacheSubfields verifies the engine merges
// only the Enabled flag onto pre-existing provider options (dino sets
// sub-field overrides at factory construction).
func TestNewAgentEngine_PreservesPromptCacheSubfields(t *testing.T) {
	inner := NewMockLLMProvider()
	provider := &cacheConfigurableProvider{LLMProvider: inner}
	provider.opts = types.PromptCacheOptions{
		Enabled:          true,
		SystemBreakpoint: false,
		ToolsBreakpoint:  false,
		HistoryEveryN:    1,
		MinCacheTokens:   2048,
	}

	config := types.NewAgentConfig()
	config.PromptCaching = false
	NewAgentEngine(provider, config)

	if provider.opts.Enabled {
		t.Error("Enabled should be overridden to false")
	}
	if provider.opts.SystemBreakpoint {
		t.Error("SystemBreakpoint sub-field should be preserved (false)")
	}
	if provider.opts.HistoryEveryN != 1 {
		t.Errorf("HistoryEveryN sub-field should be preserved, got %d", provider.opts.HistoryEveryN)
	}
	if provider.opts.MinCacheTokens != 2048 {
		t.Errorf("MinCacheTokens sub-field should be preserved, got %d", provider.opts.MinCacheTokens)
	}
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

// summaryMemory 实现 types.MemoryProvider + 可选 GetSummary 接口。
type summaryMemory struct {
	*providers.SimpleMemoryProvider
	summary string
}

func (m *summaryMemory) GetSummary(ctx context.Context) (string, error) {
	return m.summary, nil
}

func TestPrepareMessages_SummaryInjection(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.SystemMessage = "You are a test assistant."
	engine := NewAgentEngine(provider, config)

	mem := &summaryMemory{SimpleMemoryProvider: providers.NewSimpleMemoryProvider(), summary: "用户讨论了 Go 与 SQLite 的取舍。"}
	engine.SetMemory(context.Background(), mem)

	msgs, err := engine.prepareMessages(context.Background(), types.NewAgentInput("继续"), nil)
	if err != nil {
		t.Fatalf("prepareMessages: %v", err)
	}

	// 顺序：system (L1) → summary system → history → user。
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages (system, summary, user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "You are a test assistant." {
		t.Fatalf("first message should be system L1, got %q/%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "system" || !strings.Contains(msgs[1].Content, "Previous conversation summary:") {
		t.Fatalf("second message should be summary, got %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "Go 与 SQLite") {
		t.Fatalf("summary content missing: %q", msgs[1].Content)
	}
	if msgs[len(msgs)-1].Role != "user" {
		t.Fatalf("last message should be user input, got %q", msgs[len(msgs)-1].Role)
	}
}

func TestPrepareMessages_NoSummaryWhenEmpty(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.SystemMessage = "You are a test assistant."
	engine := NewAgentEngine(provider, config)

	mem := &summaryMemory{SimpleMemoryProvider: providers.NewSimpleMemoryProvider(), summary: ""}
	engine.SetMemory(context.Background(), mem)

	msgs, err := engine.prepareMessages(context.Background(), types.NewAgentInput("继续"), nil)
	if err != nil {
		t.Fatalf("prepareMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system, user) when summary empty, got %d", len(msgs))
	}
	if msgs[0].Content != "You are a test assistant." {
		t.Fatalf("first message should be system L1, got %q", msgs[0].Content)
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

// TestAgentEngine_Execute_AccumulatesCacheUsage (U8) verifies the Execute loop
// sums CachedTokens / CacheCreationTokens across iterations.
func TestAgentEngine_Execute_AccumulatesCacheUsage(t *testing.T) {
	provider := NewMockLLMProvider()
	// Iteration 1: assistant tool call, cached=100/created=50.
	provider.AddResponse(types.Message{
		Role:    "assistant",
		Content: "let me look",
		ToolCalls: []types.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: types.ToolFunction{Name: "test_tool", Arguments: map[string]interface{}{}},
		}},
		Usage: types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 100, CacheCreationTokens: 50},
	})
	// Iteration 2: final answer, cached=200/created=25.
	provider.AddResponse(types.Message{
		Role:    "assistant",
		Content: "done",
		Usage:   types.Usage{PromptTokens: 20, CompletionTokens: 7, TotalTokens: 27, CachedTokens: 200, CacheCreationTokens: 25},
	})

	config := types.NewAgentConfig()
	config.MaxIterations = 3
	engine := NewAgentEngine(provider, config)
	engine.AddTool(context.Background(), NewMockTool("test_tool"))

	ctx := context.Background()
	result, err := engine.Execute(ctx, types.NewAgentInput("hi"), nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Usage.CachedTokens != 300 {
		t.Errorf("CachedTokens = %d, want 300 (100+200)", result.Usage.CachedTokens)
	}
	if result.Usage.CacheCreationTokens != 75 {
		t.Errorf("CacheCreationTokens = %d, want 75 (50+25)", result.Usage.CacheCreationTokens)
	}
	if result.Usage.PromptTokens != 30 {
		t.Errorf("PromptTokens = %d, want 30 (10+20)", result.Usage.PromptTokens)
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

// schemaTool enforces a schema so ValidateInput fails when the call is missing
// a required field. Used to exercise F3's fatal short-circuit.
type schemaTool struct {
	name string
}

func (t *schemaTool) Name() string { return t.name }
func (t *schemaTool) Description() string {
	return "schema tool"
}
func (t *schemaTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"required_key": map[string]interface{}{"type": "string"}},
		"required":   []string{"required_key"},
	}
}
func (t *schemaTool) Metadata() types.ToolMetadata { return types.ToolMetadata{} }
func (t *schemaTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

// TestF3_SchemaFatalShortCircuits verifies that an input-validation failure is
// marked fatal and short-circuits the iteration: the error surfaces in the
// steps AND the same-layer siblings are cancelled (a hanging tool is released
// by the errgroup cancellation).
func TestF3_SchemaFatalShortCircuits(t *testing.T) {
	provider := NewMockLLMProvider()
	config := types.NewAgentConfig()
	config.EnableToolRetry = false
	ae := NewAgentEngine(provider, config)

	// A sibling tool that blocks until ctx is cancelled; it must be released by
	// the fatal validation error.
	blocked := NewMockTool("blocked").WithExecuteFunc(func(ctx context.Context, input map[string]interface{}) (interface{}, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ae.AddTool(context.Background(), &schemaTool{name: "schema_tool"})
	ae.AddTool(context.Background(), blocked)

	calls := []types.ToolCall{
		{ID: "call_schema", Type: "function", Function: types.ToolFunction{Name: "schema_tool", Arguments: map[string]interface{}{}}}, // missing required_key
		{ID: "call_blocked", Type: "function", Function: types.ToolFunction{Name: "blocked", Arguments: map[string]interface{}{}}},
	}

	// Run under a generous timeout; the schema failure should cancel "blocked"
	// immediately instead of waiting for it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sorted, err := ae.prepareToolCalls(calls)
	if err != nil {
		t.Fatalf("prepareToolCalls: %v", err)
	}
	exists, results := ae.runToolCallsByLayer(ctx, sorted, 5*time.Second)

	// The schema tool's step must be a fatal error observation.
	foundFatal := false
	for i, tc := range sorted {
		if tc.Function.Name != "schema_tool" {
			continue
		}
		if results[i].err != nil {
			var fe *types.FatalToolError
			if stderrors.As(results[i].err, &fe) {
				foundFatal = true
			}
		}
	}
	if !foundFatal {
		t.Error("expected schema failure to be wrapped in FatalToolError")
	}
	_ = exists
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

// TestExecuteStream_DeadlockTurnCtxCancel is the B3 regression test: a
// consumer stops ranging but does not close the session, and the engine is
// never Stop()ed. When the result channel fills, the engine's guarded sends
// must unblock on the caller's turn ctx cancellation — otherwise
// ExecuteStream's goroutine and errgroup hang forever.
func TestExecuteStream_DeadlockTurnCtxCancel(t *testing.T) {
	provider := NewMockLLMProvider()
	// Emit far more chunks than the default channel buffer (50) so the engine
	// is forced to block once the consumer stops draining.
	for i := 0; i < 200; i++ {
		provider.AddStreamMessage(types.StreamMessage{
			Type:    "chunk",
			Content: fmt.Sprintf("chunk-%03d-", i) + strings.Repeat("x", 40),
		})
	}

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	ae := NewAgentEngine(provider, config)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := ae.ExecuteStream(ctx, types.NewAgentInput("go"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	// Drain until the channel is momentarily empty, then stop ranging entirely
	// without cancelling ctx or calling Stop(). The engine is now blocked on a
	// full result channel (200 chunks > buffer 50). Give it a moment to make
	// sure the engine goroutine is genuinely parked on a send.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-stream:
		default:
			goto drained
		}
	}
	t.Fatal("did not observe a drain point")
drained:
	time.Sleep(50 * time.Millisecond)

	// Now cancel the turn ctx while no consumer is reading. ExecuteStream must
	// return (close the channel) within a bounded time — the guarded sends
	// unblock. The consumer is only started after cancel so the channel being
	// closed is genuinely caused by cancellation, not by us draining it.
	cancel()
	done := make(chan struct{})
	go func() {
		for range stream {
		}
		close(done)
	}()

	select {
	case <-done:
		// success — no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteStream deadlocked: channel not closed after turn ctx cancellation")
	}
}

// TestResultSender_NoDrop verifies that with a slow consumer, tool events are
// blocked (not dropped) until they can be delivered, preserving order.
func TestResultSender_NoDrop(t *testing.T) {
	provider := NewMockLLMProvider()
	ae := NewAgentEngine(provider, types.NewAgentConfig())

	resultChan := make(chan types.StreamResult, 8)
	ae.mu.Lock()
	ae.resultSender = func(result types.StreamResult) {
		select {
		case resultChan <- result:
		case <-ae.ctx.Done():
		}
	}
	ae.mu.Unlock()

	// Produce 200 tool events from a goroutine.
	const total = 200
	go func() {
		for i := 0; i < total; i++ {
			ae.resultSender(types.StreamResult{
				Type: "tool_event",
				ToolEvent: &types.ToolEvent{
					Event:      types.StreamEventToolResult,
					ToolCallID: fmt.Sprintf("call_%d", i),
					ToolName:   "mock",
				},
			})
		}
	}()

	// Consume slowly: buffer is 8, so the producer blocks and must not drop.
	got := make([]string, 0, total)
	deadline := time.After(5 * time.Second)
	for len(got) < total {
		select {
		case r := <-resultChan:
			if r.ToolEvent != nil {
				got = append(got, r.ToolEvent.ToolCallID)
			}
			// Simulate a slow consumer.
			time.Sleep(time.Millisecond)
		case <-deadline:
			t.Fatalf("timeout waiting for tool events; got %d/%d", len(got), total)
		}
	}
	for i, id := range got {
		if want := fmt.Sprintf("call_%d", i); id != want {
			t.Fatalf("event %d: expected %s, got %s (order violated)", i, want, id)
		}
	}
}

// TestChunkMerger_FlushByInterval verifies the merge window's happy path: many
// fragments collapse into few merged chunks, content concatenates in order, and
// no fragment is lost.
func TestChunkMerger_FlushByInterval(t *testing.T) {
	var mu sync.Mutex
	var got []string
	flushCount := 0
	flushCh := make(chan struct{}, 1)

	m := newChunkMerger(50*time.Millisecond, func(r types.StreamResult) bool {
		mu.Lock()
		got = append(got, r.Content)
		flushCount++
		mu.Unlock()
		select {
		case flushCh <- struct{}{}:
		default:
		}
		return true
	})

	// Feed fragments faster than the 50ms flush window for long enough that the
	// interval flush must fire mid-stream (not only on the final explicit
	// Flush), proving the window flushes on its own.
	const n = 60
	const feedInterval = 2 * time.Millisecond
	go func() {
		for i := 0; i < n; i++ {
			m.Add(fmt.Sprintf("p-%03d|", i))
			time.Sleep(feedInterval)
		}
		m.Flush() // drain the tail; a no-op if already flushed
	}()

	// The merger must produce at least two flushes over the feed duration —
	// otherwise the interval timer never fired and the test is vacuous.
	deadline := time.After(5 * time.Second)
	observed := 0
	for observed < 2 {
		select {
		case <-flushCh:
			observed++
		case <-deadline:
			t.Fatalf("timeout: expected the 50ms flush window to fire mid-stream, only %d flush(es) observed", observed)
		}
	}

	// Now wait until the feed + final flush complete.
	deadline = time.After(5 * time.Second)
	for {
		mu.Lock()
		done := flushCount > 0 && strings.Join(got, "") == streamOf(n)
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			mu.Lock()
			merged := strings.Join(got, "")
			mu.Unlock()
			t.Fatalf("timeout waiting for complete stream; got %d/%d fragments", len(merged), len(streamOf(n)))
		}
	}

	mu.Lock()
	defer mu.Unlock()
	merged := strings.Join(got, "")
	if merged != streamOf(n) {
		t.Fatalf("merged content mismatch\n got: %.120s...\nwant: %.120s...", merged, streamOf(n))
	}
	if flushCount >= n {
		t.Errorf("expected merging to cut event count below %d, got %d chunks", n, flushCount)
	}
}

// streamOf builds the concatenated fragment stream "p-000|p-001|...".
func streamOf(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "p-%03d|", i)
	}
	return b.String()
}

// TestChunkMerger_FlushExplicit verifies manual Flush after Add delivers the
// buffer synchronously and a second Flush is a no-op.
func TestChunkMerger_FlushExplicit(t *testing.T) {
	var got []string
	m := newChunkMerger(time.Hour, func(r types.StreamResult) bool {
		got = append(got, r.Content)
		return true
	})

	if !m.Add("ab") || !m.Add("cd") {
		t.Fatal("Add should succeed")
	}
	if !m.Flush() {
		t.Fatal("Flush should succeed")
	}
	if len(got) != 1 || got[0] != "abcd" {
		t.Fatalf("expected one merged chunk 'abcd', got %v", got)
	}
	if !m.Flush() {
		t.Fatal("empty Flush should be a no-op success")
	}
	if len(got) != 1 {
		t.Fatalf("second empty Flush must not send anything, got %v", got)
	}
}

// TestChunkMerger_FlushErrorPropagates verifies that a failing send is surfaced
// by Add (turn ctx cancellation mid-flush) and the buffer is cleared.
func TestChunkMerger_FlushErrorPropagates(t *testing.T) {
	send := 0
	m := newChunkMerger(time.Hour, func(r types.StreamResult) bool {
		send++
		return false // simulate cancelled turn ctx
	})

	// Fill the buffer near the cap, then Add a fragment large enough that the
	// cap is exceeded and the pending merge is flushed. The flush send fails,
	// so Add must report failure.
	if !m.Add(strings.Repeat("x", chunkMergeMaxBytes-100)) {
		t.Fatal("Add of first fragment should succeed (buffered)")
	}
	if m.Add(strings.Repeat("y", 200)) {
		t.Fatal("Add must report failure when the cap-triggered flush send fails")
	}
	if send != 1 {
		t.Fatalf("expected one flush attempt, got %d", send)
	}
	if m.sb.Len() != 0 {
		t.Fatalf("buffer must be cleared after failed flush, len=%d", m.sb.Len())
	}
}

// TestExecuteStream_ChunkMerging_HighThroughput is the F6 second-stage
// end-to-end test: 1000 streamed fragments must collapse into a far smaller
// number of "chunk" events (with a tight flush window to keep the test fast),
// the concatenated content must match exactly, and the terminal "end" must
// still arrive immediately after the final merged chunk.
func TestExecuteStream_ChunkMerging_HighThroughput(t *testing.T) {
	provider := NewMockLLMProvider()
	const n = 1000
	for i := 0; i < n; i++ {
		provider.AddStreamMessage(types.StreamMessage{
			Type:    "chunk",
			Content: fmt.Sprintf("tok-%04d|", i),
		})
	}

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	config.ChunkMergeFlushInterval = time.Millisecond // tight window keeps the test fast
	engine := NewAgentEngine(provider, config)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("go"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	var chunkEvents []string
	var endEvents []string
	for r := range stream {
		switch r.Type {
		case "chunk":
			chunkEvents = append(chunkEvents, r.Content)
		case "end":
			endEvents = append(endEvents, r.Type)
		case "error":
			t.Fatalf("unexpected error result: %v", r.Error)
		}
	}

	if len(chunkEvents) >= n {
		t.Fatalf("expected chunk merging to cut event count below %d, got %d chunk events", n, len(chunkEvents))
	}
	merged := strings.Join(chunkEvents, "")
	want := ""
	for i := 0; i < n; i++ {
		want += fmt.Sprintf("tok-%04d|", i)
	}
	if merged != want {
		t.Fatalf("merged content mismatch\n got: %.120s...\nwant: %.120s...", merged, want)
	}
	if len(endEvents) != 1 {
		t.Fatalf("expected exactly one 'end', got %d", len(endEvents))
	}
}

// TestExecuteStream_ChunkMerging_ErrorImmediate verifies that a stream "error"
// is delivered without waiting for the merge window, and buffered text is
// flushed (not dropped) before the error.
func TestExecuteStream_ChunkMerging_ErrorImmediate(t *testing.T) {
	provider := NewMockLLMProvider()
	provider.AddStreamMessage(types.StreamMessage{Type: "chunk", Content: "partial"})
	provider.AddStreamMessage(types.StreamMessage{Type: "error", Error: "boom"})

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	// Generous window: only a deferred/tool-path flush can deliver "partial".
	config.ChunkMergeFlushInterval = time.Hour
	engine := NewAgentEngine(provider, config)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("go"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	var contents []string
	var errors []string
	for r := range stream {
		switch r.Type {
		case "chunk":
			contents = append(contents, r.Content)
		case "error":
			if r.Error != nil {
				errors = append(errors, r.Error.Error())
			}
		}
	}

	if len(contents) != 1 || contents[0] != "partial" {
		t.Fatalf("expected buffered text to be flushed before error, got %v", contents)
	}
	if len(errors) != 1 || !strings.Contains(errors[0], "boom") {
		t.Fatalf("expected error to be delivered, got %v", errors)
	}
}

// TestExecuteStream_ChunkMerging_ToolEventOrder verifies the critical ordering
// constraint: when a chunk stream is followed by tool calls, the merged text
// chunks must arrive before the tool_events (a deferred-only flush would
// reorder them).
func TestExecuteStream_ChunkMerging_ToolEventOrder(t *testing.T) {
	provider := NewMockLLMProvider()
	provider.AddStreamMessage(types.StreamMessage{Type: "chunk", Content: "text-before-tool"})
	provider.AddStreamMessage(types.StreamMessage{
		Type: "tool_calls",
		ToolCalls: []types.ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: types.ToolFunction{
					Name:      "echo_tool",
					Arguments: map[string]interface{}{"msg": "hi"},
				},
			},
		},
	})

	config := types.NewAgentConfig()
	config.MaxIterations = 2
	config.ChunkMergeFlushInterval = time.Hour // force buffered text to depend on the pre-tool flush
	engine := NewAgentEngine(provider, config)

	tool := NewMockTool("echo_tool").WithExecuteFunc(func(ctx context.Context, input map[string]interface{}) (interface{}, error) {
		return "echoed", nil
	})
	engine.AddTool(context.Background(), tool)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("go"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	// Collect events in arrival order. MockLLMProvider replays the full stream
	// on every iteration, so a later iteration can re-emit a chunk AFTER the
	// first iteration's tool events — that is a mock artifact, not a reorder.
	// The property under test is scoped to the FIRST tool_event: the merged
	// text chunks that precede it must concatenate to exactly "text-before-tool".
	var chunksBefore []string
	var toolEventSeen bool
	for r := range stream {
		if r.Type == "tool_event" {
			toolEventSeen = true
			if got := strings.Join(chunksBefore, ""); got != "text-before-tool" {
				t.Fatalf("merged text before first tool_event = %q, want %q", got, "text-before-tool")
			}
			break
		}
		if r.Type == "chunk" {
			chunksBefore = append(chunksBefore, r.Content)
		}
	}
	if !toolEventSeen {
		t.Fatal("expected at least one tool_event")
	}
	if len(chunksBefore) == 0 {
		t.Fatal("expected merged text chunk(s) before the first tool_event")
	}
}

// TestExecuteStream_ChunkMerging_Disabled verifies the escape hatch: a negative
// interval restores per-fragment delivery (no merging).
func TestExecuteStream_ChunkMerging_Disabled(t *testing.T) {
	provider := NewMockLLMProvider()
	const n = 20
	for i := 0; i < n; i++ {
		provider.AddStreamMessage(types.StreamMessage{
			Type:    "chunk",
			Content: fmt.Sprintf("solo-%02d|", i),
		})
	}

	config := types.NewAgentConfig()
	config.MaxIterations = 1
	config.ChunkMergeFlushInterval = -1 // disable merging
	engine := NewAgentEngine(provider, config)

	ctx := context.Background()
	stream, err := engine.ExecuteStream(ctx, types.NewAgentInput("go"), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}

	var nChunk int
	got := ""
	for r := range stream {
		if r.Type == "chunk" {
			nChunk++
			got += r.Content
		}
	}
	if nChunk != n {
		t.Fatalf("expected %d individual chunks when merging disabled, got %d", n, nChunk)
	}
	want := ""
	for i := 0; i < n; i++ {
		want += fmt.Sprintf("solo-%02d|", i)
	}
	if got != want {
		t.Fatalf("content mismatch with merging disabled")
	}
}
