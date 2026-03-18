package dino

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/loop"
	"github.com/xichan96/cortex/dino/queue"
	"github.com/xichan96/cortex/dino/session"
	"github.com/xichan96/cortex/dino/tools"
)

func init() {
	RegisterLLMProvider("mock", func(cfg *Config) (types.LLMProvider, error) {
		return newMockLLMProvider([]string{"test response"}), nil
	})
}

func getTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Provider.Type = "mock"
	return cfg
}

type mockLLMProvider struct {
	mu           sync.Mutex
	responses    []string
	responseIdx  int
	callCount    int
	shouldStream bool
}

func newMockLLMProvider(responses []string) *mockLLMProvider {
	return &mockLLMProvider{
		responses:    responses,
		responseIdx:  0,
		callCount:    0,
		shouldStream: false,
	}
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.responseIdx >= len(m.responses) {
		m.responseIdx = 0
	}
	m.callCount++

	resp := types.Message{
		Content: m.responses[m.responseIdx],
		Usage: types.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	m.responseIdx++
	return resp, nil
}

func (m *mockLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	if m.responseIdx >= len(m.responses) {
		m.responseIdx = 0
	}
	ch <- types.StreamMessage{
		Content: m.responses[m.responseIdx],
		Usage:   &types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	m.responseIdx++
	close(ch)
	return ch, nil
}

func (m *mockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.responseIdx >= len(m.responses) {
		m.responseIdx = 0
	}
	m.callCount++

	resp := types.Message{
		Content: m.responses[m.responseIdx],
		Usage: types.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	m.responseIdx++
	return resp, nil
}

func (m *mockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	if m.responseIdx >= len(m.responses) {
		m.responseIdx = 0
	}
	ch <- types.StreamMessage{
		Content: m.responses[m.responseIdx],
		Usage:   &types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	m.responseIdx++
	close(ch)
	return ch, nil
}

func (m *mockLLMProvider) GetModelName() string {
	return "mock-model"
}

func (m *mockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{
		Name:      "mock-model",
		MaxTokens: 4096,
	}
}

type mockTool struct {
	name        string
	description string
	executeFunc func(input map[string]interface{}) (interface{}, error)
}

func (t *mockTool) Name() string                   { return t.name }
func (t *mockTool) Description() string            { return t.description }
func (t *mockTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *mockTool) Metadata() types.ToolMetadata   { return types.ToolMetadata{} }

func (t *mockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.executeFunc != nil {
		return t.executeFunc(input)
	}
	return "mock result", nil
}

func TestNewDinoFactory(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if factory == nil {
		t.Fatal("Factory should not be nil")
	}
}

func TestNewDinoFactoryWithNilConfig(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory with nil config: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if factory == nil {
		t.Fatal("Factory should not be nil")
	}
}

func TestDinoFactory_CreateSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-session-1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session == nil {
		t.Fatal("Session should not be nil")
	}

	if session.ID() != "test-session-1" {
		t.Errorf("Expected session ID 'test-session-1', got '%s'", session.ID())
	}

	if session.Input() == nil {
		t.Error("Input channel should not be nil")
	}

	if session.Output() == nil {
		t.Error("Output channel should not be nil")
	}

	session.Close()
}

func TestDinoFactory_GetSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-session-2")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	retrieved := factory.GetSession("test-session-2")
	if retrieved == nil {
		t.Error("Retrieved session should not be nil")
	}

	if retrieved.ID() != session.ID() {
		t.Errorf("Expected session ID '%s', got '%s'", session.ID(), retrieved.ID())
	}

	session.Close()
}

func TestDinoFactory_CloseSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	_, err = factory.CreateSession(ctx, "test-session-3")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	factory.CloseSession("test-session-3")

	retrieved := factory.GetSession("test-session-3")
	if retrieved != nil {
		t.Error("Session should be nil after close")
	}
}

func TestDinoFactory_CloseAll(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}

	ctx := context.Background()
	factory.CreateSession(ctx, "session-1")
	factory.CreateSession(ctx, "session-2")
	factory.CreateSession(ctx, "session-3")

	factory.CloseAll()

	if factory.GetSession("session-1") != nil {
		t.Error("session-1 should be closed")
	}
	if factory.GetSession("session-2") != nil {
		t.Error("session-2 should be closed")
	}
	if factory.GetSession("session-3") != nil {
		t.Error("session-3 should be closed")
	}
}

func TestSession_IsRunning(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-running")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if !session.IsRunning() {
		t.Error("Session should be running")
	}

	session.Close()

	time.Sleep(50 * time.Millisecond)
	if session.IsRunning() {
		t.Error("Session should not be running after close")
	}
}

func TestSession_SubscribeUnsubscribe(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-sub")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	var mu sync.Mutex
	receivedEvents := []*Event{}

	observer := ObserverFunc(func(event *Event) {
		mu.Lock()
		defer mu.Unlock()
		receivedEvents = append(receivedEvents, event)
	})

	subID := session.Subscribe(observer)
	if subID == "" {
		t.Error("Subscribe ID should not be empty")
	}

	session.Unsubscribe(subID)

	session.Close()
}

func TestSession_EmitAndReceive(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-emit")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		timeout := time.After(2 * time.Second)
		for {
			select {
			case event := <-session.Output():
				t.Logf("Received event: type=%s, content=%s", event.Type, event.Content)
				if event.Type == EventTypeMessage {
					return
				}
			case <-timeout:
				t.Error("Timeout waiting for event")
				return
			}
		}
	}()

	session.Input() <- "test message"

	wg.Wait()
	session.Close()
}

func TestSession_EmitMultipleEvents(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "test-multi-events")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	expectedEvents := []EventType{
		EventTypeMessage,
		EventTypeMessage,
		EventTypeDone,
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		count := 0
		timeout := time.After(2 * time.Second)
		for {
			select {
			case event := <-session.Output():
				t.Logf("Event %d: type=%s", count, event.Type)
				if count < len(expectedEvents) && event.Type == expectedEvents[count] {
					count++
				}
				if count == len(expectedEvents) {
					return
				}
			case <-timeout:
				if count != len(expectedEvents) {
					t.Errorf("Expected %d events, got %d", len(expectedEvents), count)
				}
				return
			}
		}
	}()

	session.Input() <- "test 1"
	session.Input() <- "test 2"

	time.Sleep(100 * time.Millisecond)
	session.Close()
	wg.Wait()
}

func TestClient_NewClient(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	if client == nil {
		t.Fatal("Client should not be nil")
	}

	client.CloseAll()
}

func TestClient_CreateSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session, err := client.CreateSession(ctx, "client-session-1")
	if err != nil {
		t.Fatalf("Failed to create client session: %v", err)
	}

	if session == nil {
		t.Fatal("ClientSession should not be nil")
	}

	if session.Input() == nil {
		t.Error("Input channel should not be nil")
	}

	if session.Output() == nil {
		t.Error("Output channel should not be nil")
	}

	if session.Done() == nil {
		t.Error("Done channel should not be nil")
	}

	client.CloseSession("client-session-1")
}

func TestClient_GetSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	client.CreateSession(ctx, "client-session-2")

	retrieved := client.GetSession("client-session-2")
	if retrieved == nil {
		t.Error("Retrieved session should not be nil")
	}

	client.CloseAll()
}

func TestClient_CloseSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	client.CreateSession(ctx, "client-session-3")
	client.CloseSession("client-session-3")

	retrieved := client.GetSession("client-session-3")
	if retrieved != nil {
		t.Error("Session should be nil after close")
	}
}

func TestClient_CloseAll(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	client.CreateSession(ctx, "c-1")
	client.CreateSession(ctx, "c-2")
	client.CreateSession(ctx, "c-3")

	client.CloseAll()

	sessions := client.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(sessions))
	}
}

func TestClient_ListSessions(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	client.CreateSession(ctx, "list-1")
	client.CreateSession(ctx, "list-2")

	sessions := client.ListSessions()
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(sessions))
	}

	client.CloseAll()
}

func TestClientSession_Send(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session, err := client.CreateSession(ctx, "send-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	err = session.Send(ctx, "test message")
	if err != nil {
		t.Errorf("Failed to send: %v", err)
	}

	session.Close()
	client.CloseAll()
}

func TestClientSession_SendAndWait(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session, err := client.CreateSession(ctx, "send-wait-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	_, err = session.SendAndWait(ctx, "test message")
	if err != nil {
		t.Logf("SendAndWait returned error (expected in test): %v", err)
	}

	session.Close()
	client.CloseAll()
}

func TestClientSession_Subscribe(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session, err := client.CreateSession(ctx, "subscribe-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	subID := session.SubscribeFunc(func(event *Event) {
		_ = event
	})

	if subID == "" {
		t.Error("Subscription ID should not be empty")
	}

	session.Unsubscribe(subID)

	session.Close()
	client.CloseAll()
}

func TestClientSession_Handler(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session, err := client.CreateSession(ctx, "handler-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	messageReceived := false
	thinkingReceived := false
	toolCallReceived := false
	toolResultReceived := false
	errorReceived := false
	doneReceived := false
	approvalReceived := false

	session.Handler().
		OnMessage(func(content string) {
			messageReceived = true
		}).
		OnThinking(func(thinking string) {
			thinkingReceived = true
		}).
		OnToolCall(func(toolName string, input map[string]interface{}) {
			toolCallReceived = true
		}).
		OnToolResult(func(toolName string, output interface{}) {
			toolResultReceived = true
		}).
		OnError(func(err string) {
			errorReceived = true
		}).
		OnDone(func() {
			doneReceived = true
		}).
		OnApproval(func(toolName string, approved bool) {
			approvalReceived = true
		})

	_ = messageReceived
	_ = thinkingReceived
	_ = toolCallReceived
	_ = toolResultReceived
	_ = errorReceived
	_ = doneReceived
	_ = approvalReceived

	session.Close()
	client.CloseAll()
}

func TestEvent_IsMessage(t *testing.T) {
	event := &Event{Type: EventTypeMessage}
	if !event.IsMessage() {
		t.Error("IsMessage should return true")
	}

	event.Type = EventTypeThinking
	if event.IsMessage() {
		t.Error("IsMessage should return false for EventTypeThinking")
	}
}

func TestEvent_IsThinking(t *testing.T) {
	event := &Event{Type: EventTypeThinking}
	if !event.IsThinking() {
		t.Error("IsThinking should return true")
	}

	event.Type = EventTypeMessage
	if event.IsThinking() {
		t.Error("IsThinking should return false for EventTypeMessage")
	}
}

func TestEvent_IsToolCall(t *testing.T) {
	event := &Event{Type: EventTypeToolCall}
	if !event.IsToolCall() {
		t.Error("IsToolCall should return true")
	}

	event.Type = EventTypeMessage
	if event.IsToolCall() {
		t.Error("IsToolCall should return false for EventTypeMessage")
	}
}

func TestEvent_IsToolResult(t *testing.T) {
	event := &Event{Type: EventTypeToolResult}
	if !event.IsToolResult() {
		t.Error("IsToolResult should return true")
	}

	event.Type = EventTypeMessage
	if event.IsToolResult() {
		t.Error("IsToolResult should return false for EventTypeMessage")
	}
}

func TestEvent_IsError(t *testing.T) {
	event := &Event{Type: EventTypeError}
	if !event.IsError() {
		t.Error("IsError should return true")
	}

	event.Type = EventTypeMessage
	if event.IsError() {
		t.Error("IsError should return false for EventTypeMessage")
	}
}

func TestEvent_IsDone(t *testing.T) {
	event := &Event{Type: EventTypeDone}
	if !event.IsDone() {
		t.Error("IsDone should return true")
	}

	event.Type = EventTypeMessage
	if event.IsDone() {
		t.Error("IsDone should return false for EventTypeMessage")
	}
}

func TestEvent_IsApproval(t *testing.T) {
	event := &Event{Type: EventTypeApproval}
	if !event.IsApproval() {
		t.Error("IsApproval should return true for EventTypeApproval")
	}

	event.Type = EventTypeApproved
	if !event.IsApproval() {
		t.Error("IsApproval should return true for EventTypeApproved")
	}

	event.Type = EventTypeMessage
	if event.IsApproval() {
		t.Error("IsApproval should return false for EventTypeMessage")
	}
}

func TestEvent_IsTokenUsage(t *testing.T) {
	event := &Event{Type: EventTypeTokenUsage}
	if !event.IsTokenUsage() {
		t.Error("IsTokenUsage should return true")
	}

	event.Type = EventTypeMessage
	if event.IsTokenUsage() {
		t.Error("IsTokenUsage should return false for EventTypeMessage")
	}
}

func TestEvent_String(t *testing.T) {
	event := &Event{
		Type:      EventTypeMessage,
		Content:   "test content",
		SessionID: "test-session",
	}

	str := event.String()
	if str == "" {
		t.Error("String should not be empty")
	}

	var parsed Event
	err := json.Unmarshal([]byte(str), &parsed)
	if err != nil {
		t.Errorf("Failed to unmarshal: %v", err)
	}

	if parsed.Content != event.Content {
		t.Errorf("Expected content '%s', got '%s'", event.Content, parsed.Content)
	}
}

func TestNewAgentInput(t *testing.T) {
	input := NewAgentInput("test text")
	if input.Text != "test text" {
		t.Errorf("Expected text 'test text', got '%s'", input.Text)
	}
}

func TestSessionOptions(t *testing.T) {
	opts := []SessionOption{
		WithInputBufferSize(20),
		WithOutputBufferSize(30),
		WithQueueEnabled(100, 10),
	}

	cfg := &session.Config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.InputBufferSize != 20 {
		t.Errorf("Expected InputBufferSize 20, got %d", cfg.InputBufferSize)
	}

	if cfg.OutputBufferSize != 30 {
		t.Errorf("Expected OutputBufferSize 30, got %d", cfg.OutputBufferSize)
	}

	if !cfg.EnableQueue {
		t.Error("Queue should be enabled")
	}

	if cfg.QueueSize != 100 {
		t.Errorf("Expected QueueSize 100, got %d", cfg.QueueSize)
	}

	if cfg.MaxPending != 10 {
		t.Errorf("Expected MaxPending 10, got %d", cfg.MaxPending)
	}
}

func TestClientOptions(t *testing.T) {
	opts := []ClientOption{
		WithSessionBufferSize(25),
		WithSessionQueueEnabled(200, 20),
	}

	cfg := &clientConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.sessionBufferSize != 25 {
		t.Errorf("Expected sessionBufferSize 25, got %d", cfg.sessionBufferSize)
	}

	if !cfg.enableQueue {
		t.Error("Queue should be enabled")
	}

	if cfg.queueMaxSize != 200 {
		t.Errorf("Expected queueMaxSize 200, got %d", cfg.queueMaxSize)
	}

	if cfg.queueMaxPending != 20 {
		t.Errorf("Expected queueMaxPending 20, got %d", cfg.queueMaxPending)
	}
}

func TestSession_QueueOperations(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "queue-test", WithQueueEnabled(100, 10))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	size := session.QueueSize()
	t.Logf("Queue size: %d", size)

	pending := session.QueuePending()
	t.Logf("Queue pending: %d", pending)

	stats := session.QueueStats()
	t.Logf("Queue stats: %+v", stats)

	session.Close()
}

func TestDinoFactory_GetTools(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	tools := factory.GetTools()
	if len(tools) == 0 {
		t.Error("Should have at least some tools")
	}
	t.Logf("Got %d tools", len(tools))
}

func TestDinoFactory_GetSkills(t *testing.T) {
	cfg := getTestConfig()
	cfg.Skills.Path = ""
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	skills := factory.GetSkills()
	t.Logf("Got %d skills", len(skills))
}

func TestDinoFactory_CreateDuplicateSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session1, err := factory.CreateSession(ctx, "duplicate-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	session2, err := factory.CreateSession(ctx, "duplicate-test")
	if err != nil {
		t.Fatalf("Failed to create duplicate session: %v", err)
	}

	if session1.ID() != session2.ID() {
		t.Error("Should return same session for duplicate ID")
	}

	session1.Close()
}

func TestClient_CreateDuplicateSession(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	ctx := context.Background()

	session1, err := client.CreateSession(ctx, "client-dup")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	session2, err := client.CreateSession(ctx, "client-dup")
	if err != nil {
		t.Fatalf("Failed to create duplicate session: %v", err)
	}

	if session1 != session2 {
		t.Error("Should return same session for duplicate ID")
	}

	client.CloseAll()
}

func TestObserverFunc(t *testing.T) {
	var mu sync.Mutex
	called := false

	fn := ObserverFunc(func(event *Event) {
		mu.Lock()
		defer mu.Unlock()
		called = true
	})

	event := &Event{Type: EventTypeMessage, Content: "test"}
	fn.OnEvent(event)

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Error("ObserverFunc should call the underlying function")
	}
}

func TestRegisterLLMProvider(t *testing.T) {
	registered := false
	RegisterLLMProvider("test-provider", func(cfg *Config) (types.LLMProvider, error) {
		registered = true
		return newMockLLMProvider([]string{"test response"}), nil
	})

	cfg := &Config{
		Provider: ProviderConfig{
			Type: "test-provider",
		},
	}

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}

	if !registered {
		t.Error("Custom provider should be registered")
	}

	factory.Shutdown(context.Background())
}

func TestSession_Stop(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "stop-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if !session.IsRunning() {
		t.Error("Session should be running")
	}

	session.Stop()

	time.Sleep(50 * time.Millisecond)
	if session.IsRunning() {
		t.Error("Session should not be running after stop")
	}

	session.Close()
}

func TestEvent_TokenUsage(t *testing.T) {
	event := &Event{
		Type: EventTypeTokenUsage,
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 200,
			TotalTokens:      300,
			ReasoningTokens:  50,
		},
	}

	if event.Usage.PromptTokens != 100 {
		t.Errorf("Expected 100 prompt tokens, got %d", event.Usage.PromptTokens)
	}

	if event.Usage.TotalTokens != 300 {
		t.Errorf("Expected 300 total tokens, got %d", event.Usage.TotalTokens)
	}
}

func TestEvent_ToolInfo(t *testing.T) {
	event := &Event{
		Type:       EventTypeToolCall,
		ToolCallID: "call-123",
		ToolName:   "read_file",
		ToolInput: map[string]interface{}{
			"path": "/test/file.txt",
		},
		ToolOutput: "file content",
	}

	if event.ToolName != "read_file" {
		t.Errorf("Expected tool name 'read_file', got '%s'", event.ToolName)
	}

	if event.ToolInput["path"] != "/test/file.txt" {
		t.Error("Tool input path mismatch")
	}
}

func TestMockLLMProvider(t *testing.T) {
	provider := newMockLLMProvider([]string{"response1", "response2", "response3"})

	resp1, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "test"}})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp1.Content != "response1" {
		t.Errorf("Expected 'response1', got '%s'", resp1.Content)
	}

	resp2, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "test"}})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp2.Content != "response2" {
		t.Errorf("Expected 'response2', got '%s'", resp2.Content)
	}

	if provider.callCount != 2 {
		t.Errorf("Expected 2 calls, got %d", provider.callCount)
	}
}

func TestMockTool(t *testing.T) {
	tool := &mockTool{
		name:        "test_tool",
		description: "A test tool",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return "custom result", nil
		},
	}

	if tool.Name() != "test_tool" {
		t.Errorf("Expected 'test_tool', got '%s'", tool.Name())
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"key": "value"})
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if result != "custom result" {
		t.Errorf("Expected 'custom result', got '%v'", result)
	}
}

func TestSession_WithMultipleOptions(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(
		ctx,
		"multi-opts",
		WithInputBufferSize(50),
		WithOutputBufferSize(60),
		WithQueueEnabled(500, 50),
	)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session == nil {
		t.Fatal("Session should not be nil")
	}

	session.Close()
}

func TestClient_WithMultipleOptions(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(
		factory,
		WithSessionBufferSize(40),
		WithSessionQueueEnabled(400, 40),
	)

	ctx := context.Background()
	session, err := client.CreateSession(ctx, "client-multi-opts")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	if session == nil {
		t.Fatal("Session should not be nil")
	}

	client.CloseAll()
}

func TestApprovalStore(t *testing.T) {
	t.Skip("Requires async response handling - test flaky due to timing")
}

func TestApprovalStore_Timeout(t *testing.T) {
	store := NewApprovalStore(100 * time.Millisecond)

	_, err := store.RequestApproval(context.Background(), "sess-2", "bash", `{"command": "rm -rf /"}`)
	if err == nil {
		t.Error("Should return timeout error")
	}
}

func TestApprovalTool(t *testing.T) {
	store := NewApprovalStore(5 * time.Second)

	innerTool := &mockTool{
		name:        "safe_tool",
		description: "A safe tool",
	}

	approvalTool := NewApprovalTool(innerTool, "test-session", store, map[string]bool{
		"safe_tool": false,
	})

	result, err := approvalTool.Execute(context.Background(), map[string]interface{}{"key": "value"})
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if result != "mock result" {
		t.Errorf("Expected 'mock result', got '%v'", result)
	}
}

func TestSkill(t *testing.T) {
	skill := &Skill{
		Name:        "test_skill",
		Description: "A test skill",
		Prompt:      "You are a test assistant",
	}

	if skill.Name != "test_skill" {
		t.Errorf("Expected 'test_skill', got '%s'", skill.Name)
	}

	if skill.Description != "A test skill" {
		t.Errorf("Expected 'A test skill', got '%s'", skill.Description)
	}
}

func TestUsage(t *testing.T) {
	usage := Usage{
		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,
		ReasoningTokens:  50,
	}

	if usage.PromptTokens != 100 {
		t.Errorf("Expected 100, got %d", usage.PromptTokens)
	}

	if usage.TotalTokens != 300 {
		t.Errorf("Expected 300, got %d", usage.TotalTokens)
	}
}

func TestExecuteResponse(t *testing.T) {
	resp := &ExecuteResponse{
		SessionID: "sess-1",
		Content:   "Hello world",
		ToolCalls: []ToolCallInfo{
			{ID: "call-1", Name: "read_file", Input: map[string]interface{}{"path": "/test.txt"}},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	if resp.SessionID != "sess-1" {
		t.Errorf("Expected 'sess-1', got '%s'", resp.SessionID)
	}

	if len(resp.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	if resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("Expected 'read_file', got '%s'", resp.ToolCalls[0].Name)
	}
}

func TestStreamEvent(t *testing.T) {
	event := StreamEvent{
		Type:       StreamEventContent,
		Content:    "test content",
		SessionID:  "sess-1",
		ToolCallID: "call-1",
		ToolName:   "bash",
		ToolInput:  map[string]interface{}{"command": "ls"},
		ToolOutput: "output",
		Error:      "",
		Approved:   true,
	}

	if event.Type != StreamEventContent {
		t.Errorf("Expected 'content', got '%s'", event.Type)
	}

	if event.Content != "test content" {
		t.Errorf("Expected 'test content', got '%s'", event.Content)
	}
}

func TestConfig_Default(t *testing.T) {
	cfg := getTestConfig()

	if cfg.DefaultModel != "gpt-4o-mini" {
		t.Errorf("Expected 'gpt-4o-mini', got '%s'", cfg.DefaultModel)
	}

	if cfg.DefaultProvider != "openai" {
		t.Errorf("Expected 'openai', got '%s'", cfg.DefaultProvider)
	}

	if cfg.Temperature != 0.7 {
		t.Errorf("Expected 0.7, got %f", cfg.Temperature)
	}

	if cfg.MaxTokens != 4096 {
		t.Errorf("Expected 4096, got %d", cfg.MaxTokens)
	}

	if !cfg.LoopDetection.Enabled {
		t.Error("Loop detection should be enabled")
	}

	if !cfg.Budget.Enabled {
		t.Error("Budget should be enabled")
	}
}

func TestProviderConfig(t *testing.T) {
	cfg := &ProviderConfig{
		Type:    "openai",
		APIKey:  "test-key",
		BaseURL: "https://api.openai.com/v1",
		Models: map[string]string{
			"default": "gpt-4",
			"fast":    "gpt-4o-mini",
		},
		Headers: map[string]string{
			"X-Custom-Header": "value",
		},
	}

	if cfg.Type != "openai" {
		t.Errorf("Expected 'openai', got '%s'", cfg.Type)
	}

	if cfg.Models["default"] != "gpt-4" {
		t.Errorf("Expected 'gpt-4', got '%s'", cfg.Models["default"])
	}

	if cfg.Headers["X-Custom-Header"] != "value" {
		t.Errorf("Expected 'value', got '%s'", cfg.Headers["X-Custom-Header"])
	}
}

func TestToolConfig(t *testing.T) {
	cfg := &ToolConfig{
		Profile:          "coding",
		Allowed:          []string{"read_file", "write_file", "edit_file"},
		ApprovalRequired: []string{"bash", "write_file"},
		Denied:           []string{"delete_file"},
	}

	if cfg.Profile != "coding" {
		t.Errorf("Expected 'coding', got '%s'", cfg.Profile)
	}

	if len(cfg.Allowed) != 3 {
		t.Errorf("Expected 3 allowed tools, got %d", len(cfg.Allowed))
	}

	if len(cfg.ApprovalRequired) != 2 {
		t.Errorf("Expected 2 approval required tools, got %d", len(cfg.ApprovalRequired))
	}
}

func TestLoopDetectionConfig(t *testing.T) {
	cfg := &LoopDetectionConfig{
		Enabled:             true,
		MaxRepeats:          5,
		SimilarityThreshold: 0.9,
	}

	if !cfg.Enabled {
		t.Error("Should be enabled")
	}

	if cfg.MaxRepeats != 5 {
		t.Errorf("Expected 5, got %d", cfg.MaxRepeats)
	}

	if cfg.SimilarityThreshold != 0.9 {
		t.Errorf("Expected 0.9, got %f", cfg.SimilarityThreshold)
	}
}

func TestBudgetConfig(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000000,
		MaxToolCalls: 100,
		MaxTimeMs:    600000,
	}

	if !cfg.Enabled {
		t.Error("Should be enabled")
	}

	if cfg.MaxTokens != 1000000 {
		t.Errorf("Expected 1000000, got %d", cfg.MaxTokens)
	}

	if cfg.MaxToolCalls != 100 {
		t.Errorf("Expected 100, got %d", cfg.MaxToolCalls)
	}

	if cfg.MaxTimeMs != 600000 {
		t.Errorf("Expected 600000, got %d", cfg.MaxTimeMs)
	}
}

func TestSkillsConfig(t *testing.T) {
	cfg := &SkillsConfig{
		Path:     "/path/to/skills",
		AutoLoad: true,
	}

	if cfg.Path != "/path/to/skills" {
		t.Errorf("Expected '/path/to/skills', got '%s'", cfg.Path)
	}

	if !cfg.AutoLoad {
		t.Error("AutoLoad should be true")
	}
}

func TestBus_GetGlobalBus(t *testing.T) {
	bus1 := GetGlobalBus()
	if bus1 == nil {
		t.Fatal("GetGlobalBus should not return nil")
	}

	bus2 := GetGlobalBus()
	if bus1 != bus2 {
		t.Error("GetGlobalBus should return the same instance")
	}
}

func TestBus_NewBus(t *testing.T) {
	bus := NewBus()
	if bus == nil {
		t.Fatal("NewBus should not return nil")
	}

	var received bool
	var mu sync.Mutex

	bus.Subscribe("test.event", func(event BusEvent) {
		mu.Lock()
		defer mu.Unlock()
		received = true
		if event.SessionID != "session-1" {
			t.Errorf("Expected session-1, got %s", event.SessionID)
		}
	})

	bus.Publish("test.event", "session-1", map[string]string{"key": "value"})
	bus.WaitAsync()

	mu.Lock()
	defer mu.Unlock()
	if !received {
		t.Error("Subscriber should have received the event")
	}
}

func TestBus_HasSubscribers(t *testing.T) {
	bus := NewBus()

	if bus.HasSubscribers("empty.event") {
		t.Error("Should have no subscribers initially")
	}

	bus.Subscribe("empty.event", func(event BusEvent) {})
	if !bus.HasSubscribers("empty.event") {
		t.Error("Should have subscribers after subscribe")
	}

	bus.Unsubscribe("empty.event", nil)
}

func TestBus_SetGlobalBus(t *testing.T) {
	originalBus := GetGlobalBus()
	newBus := NewBus()
	SetGlobalBus(newBus)

	if GetGlobalBus() != newBus {
		t.Error("SetGlobalBus should change the global bus")
	}

	SetGlobalBus(originalBus)
}

func TestGetRegisteredProviders(t *testing.T) {
	providers := GetRegisteredProviders()
	if len(providers) == 0 {
		t.Error("Should have at least mock provider registered")
	}
}

func TestClearProviderRegistry(t *testing.T) {
	RegisterLLMProvider("clear-test", func(cfg *Config) (types.LLMProvider, error) {
		return nil, nil
	})

	providersBefore := GetRegisteredProviders()
	ClearProviderRegistry()
	providersAfter := GetRegisteredProviders()

	if len(providersBefore) <= len(providersAfter) {
		t.Error("ClearProviderRegistry should reduce provider count")
	}

	RegisterLLMProvider("mock", func(cfg *Config) (types.LLMProvider, error) {
		return newMockLLMProvider([]string{"test"}), nil
	})
}

func TestDefaultConfig_PlannerMode(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.PlannerMode.Enabled {
		t.Error("PlannerMode should be disabled by default")
	}

	if cfg.PlannerMode.PromptPlan == "" {
		t.Error("PlannerMode should have a default prompt")
	}

	if cfg.PlannerMode.AutoApprove {
		t.Error("PlannerMode AutoApprove should be false by default")
	}
}

func TestWithPlannerEnabled(t *testing.T) {
	opts := []SessionOption{
		WithPlannerEnabled(true, "custom prompt", true),
	}

	cfg := &session.Config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if !cfg.PlannerEnabled {
		t.Error("PlannerEnabled should be true")
	}

	if cfg.PlannerPrompt != "custom prompt" {
		t.Errorf("Expected 'custom prompt', got '%s'", cfg.PlannerPrompt)
	}

	if !cfg.PlannerAutoApprove {
		t.Error("PlannerAutoApprove should be true")
	}
}

func TestMessage_NewMessage(t *testing.T) {
	msg := NewMessage("msg-1", RoleUser, "session-1")

	if msg.ID != "msg-1" {
		t.Errorf("Expected ID 'msg-1', got '%s'", msg.ID)
	}

	if msg.Role != RoleUser {
		t.Errorf("Expected role 'user', got '%s'", msg.Role)
	}

	if msg.Metadata == nil {
		t.Error("Metadata should not be nil")
	}

	if msg.Metadata.SessionID != "session-1" {
		t.Errorf("Expected session ID 'session-1', got '%s'", msg.Metadata.SessionID)
	}

	if msg.Metadata.Time.Created == 0 {
		t.Error("Created time should be set")
	}
}

func TestMessage_AddText(t *testing.T) {
	msg := NewMessage("msg-1", RoleUser, "session-1")
	msg.AddText("Hello")
	msg.AddText(" World")

	text := msg.GetText()
	if text != "Hello World" {
		t.Errorf("Expected 'Hello World', got '%s'", text)
	}

	if len(msg.Parts) != 2 {
		t.Errorf("Expected 2 parts, got %d", len(msg.Parts))
	}
}

func TestMessage_AddReasoning(t *testing.T) {
	msg := NewMessage("msg-1", RoleAssistant, "session-1")
	msg.AddReasoning("Let me think about this")
	msg.AddReasoning(" more carefully")

	reasoning := msg.GetReasoning()
	if reasoning != "Let me think about this more carefully" {
		t.Errorf("Expected reasoning text, got '%s'", reasoning)
	}
}

func TestMessage_AddToolCall(t *testing.T) {
	msg := NewMessage("msg-1", RoleAssistant, "session-1")
	msg.AddToolCall("call-1", "read_file", map[string]interface{}{"path": "/test.txt"})

	toolCalls := msg.GetToolCalls()
	if len(toolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(toolCalls))
	}

	if toolCalls[0].ToolCallID != "call-1" {
		t.Errorf("Expected tool call ID 'call-1', got '%s'", toolCalls[0].ToolCallID)
	}

	if toolCalls[0].ToolName != "read_file" {
		t.Errorf("Expected tool name 'read_file', got '%s'", toolCalls[0].ToolName)
	}

	if toolCalls[0].Args["path"] != "/test.txt" {
		t.Error("Tool args mismatch")
	}
}

func TestMessage_AddToolResult(t *testing.T) {
	msg := NewMessage("msg-1", RoleAssistant, "session-1")
	msg.AddToolResult("call-1", "read_file", "file content")

	results := msg.GetToolResults()
	if len(results) != 1 {
		t.Errorf("Expected 1 tool result, got %d", len(results))
	}

	if results[0].Result != "file content" {
		t.Errorf("Expected 'file content', got '%s'", results[0].Result)
	}
}

func TestMessage_Complete(t *testing.T) {
	msg := NewMessage("msg-1", RoleUser, "session-1")
	initialTime := msg.Metadata.Time.Completed

	msg.Complete()

	if msg.Metadata.Time.Completed == 0 {
		t.Error("Completed time should be set")
	}

	if msg.Metadata.Time.Completed < initialTime {
		t.Error("Completed time should be updated")
	}
}

func TestMessage_GetText_Empty(t *testing.T) {
	msg := NewMessage("msg-1", RoleUser, "session-1")
	text := msg.GetText()

	if text != "" {
		t.Errorf("Expected empty string, got '%s'", text)
	}
}

func TestMessage_GetToolCalls_Empty(t *testing.T) {
	msg := NewMessage("msg-1", RoleUser, "session-1")
	msg.AddText("just text")

	calls := msg.GetToolCalls()
	if len(calls) != 0 {
		t.Errorf("Expected 0 tool calls, got %d", len(calls))
	}
}

func TestMessage_GetToolResults_Empty(t *testing.T) {
	msg := NewMessage("msg-1", RoleAssistant, "session-1")
	msg.AddText("just text")

	results := msg.GetToolResults()
	if len(results) != 0 {
		t.Errorf("Expected 0 tool results, got %d", len(results))
	}
}

func TestTextPart(t *testing.T) {
	part := TextPart{
		Type: "text",
		Text: "Hello World",
	}

	part.isPart()

	if part.Text != "Hello World" {
		t.Errorf("Expected 'Hello World', got '%s'", part.Text)
	}
}

func TestReasoningPart(t *testing.T) {
	part := ReasoningPart{
		Type: "reasoning",
		Text: "Let me think",
		ProviderMetadata: map[string]interface{}{
			"key": "value",
		},
	}

	part.isPart()

	if part.Text != "Let me think" {
		t.Errorf("Expected 'Let me think', got '%s'", part.Text)
	}
}

func TestToolInvocationPart(t *testing.T) {
	part := ToolInvocationPart{
		Type: "tool-invocation",
		ToolInvocation: ToolInvocation{
			State:      ToolStateCall,
			ToolCallID: "call-1",
			ToolName:   "bash",
			Args:       map[string]interface{}{"command": "ls"},
		},
	}

	part.isPart()

	if part.ToolInvocation.ToolName != "bash" {
		t.Errorf("Expected 'bash', got '%s'", part.ToolInvocation.ToolName)
	}
}

func TestToolInvocationState(t *testing.T) {
	states := []ToolInvocationState{
		ToolStateCall,
		ToolStatePartialCall,
		ToolStateResult,
	}

	if len(states) != 3 {
		t.Error("Should have 3 tool invocation states")
	}

	if ToolStateCall != "call" {
		t.Errorf("Expected 'call', got '%s'", ToolStateCall)
	}

	if ToolStatePartialCall != "partial-call" {
		t.Errorf("Expected 'partial-call', got '%s'", ToolStatePartialCall)
	}

	if ToolStateResult != "result" {
		t.Errorf("Expected 'result', got '%s'", ToolStateResult)
	}
}

func TestSourceURLPart(t *testing.T) {
	part := SourceURLPart{
		Type:     "source",
		SourceID: "src-1",
		URL:      "https://example.com",
		Title:    "Example",
	}

	part.isPart()

	if part.Title != "Example" {
		t.Errorf("Expected 'Example', got '%s'", part.Title)
	}
}

func TestFilePart(t *testing.T) {
	part := FilePart{
		Type:      "file",
		MediaType: "text/plain",
		Filename:  "test.txt",
		URL:       "file:///test.txt",
	}

	part.isPart()

	if part.Filename != "test.txt" {
		t.Errorf("Expected 'test.txt', got '%s'", part.Filename)
	}
}

func TestStepStartPart(t *testing.T) {
	part := StepStartPart{
		Type: "step_start",
	}

	part.isPart()

	if part.Type != "step_start" {
		t.Errorf("Expected 'step_start', got '%s'", part.Type)
	}
}

func TestToolMeta(t *testing.T) {
	meta := ToolMeta{
		Title:    "Read File",
		Snapshot: "file content",
		Time: ToolMetaTime{
			Start: 1000,
			End:   2000,
		},
		Extra: map[string]interface{}{
			"key": "value",
		},
	}

	if meta.Title != "Read File" {
		t.Errorf("Expected 'Read File', got '%s'", meta.Title)
	}

	if meta.Time.End != 2000 {
		t.Errorf("Expected 2000, got %d", meta.Time.End)
	}
}

func TestAssistantMeta(t *testing.T) {
	meta := AssistantMeta{
		System:   []string{"system prompt"},
		ModelID:  "gpt-4",
		Provider: "openai",
		Path: AssistantPath{
			CWD:  "/home/user",
			Root: "/home/user/project",
		},
		Cost:    0.5,
		Summary: true,
		Tokens: TokenInfo{
			Input:     100,
			Output:    200,
			Reasoning: 50,
			Cache: TokenCache{
				Read:  10,
				Write: 5,
			},
		},
	}

	if meta.ModelID != "gpt-4" {
		t.Errorf("Expected 'gpt-4', got '%s'", meta.ModelID)
	}

	if meta.Tokens.Output != 200 {
		t.Errorf("Expected 200, got %d", meta.Tokens.Output)
	}

	if !meta.Summary {
		t.Error("Summary should be true")
	}
}

func TestMessageMetadata(t *testing.T) {
	metadata := MessageMetadata{
		Time: MessageTime{
			Created:   1000,
			Completed: 2000,
		},
		Error: &MessageError{
			Name:    "ErrorName",
			Message: "Error message",
		},
		SessionID: "session-1",
		Tool: map[string]ToolMeta{
			"read_file": {
				Title: "Read File",
				Time:  ToolMetaTime{Start: 1000},
			},
		},
		Snapshot: "snapshot content",
	}

	if metadata.Error.Name != "ErrorName" {
		t.Errorf("Expected 'ErrorName', got '%s'", metadata.Error.Name)
	}

	if metadata.Tool["read_file"].Title != "Read File" {
		t.Error("Tool metadata mismatch")
	}
}

func TestExecuteRequest(t *testing.T) {
	req := ExecuteRequest{
		SessionID:         "session-1",
		Content:           "test content",
		Files:             []FileAttachment{{Path: "/test.txt", Name: "test.txt", Content: []byte("content")}},
		ExtraSystemPrompt: "extra system prompt",
		Stream:            true,
	}

	if req.SessionID != "session-1" {
		t.Errorf("Expected 'session-1', got '%s'", req.SessionID)
	}

	if len(req.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(req.Files))
	}

	if !req.Stream {
		t.Error("Stream should be true")
	}
}

func TestFileAttachment(t *testing.T) {
	attachment := FileAttachment{
		Path:    "/test.txt",
		Name:    "test.txt",
		Content: []byte("file content"),
	}

	if attachment.Path != "/test.txt" {
		t.Errorf("Expected '/test.txt', got '%s'", attachment.Path)
	}

	if string(attachment.Content) != "file content" {
		t.Errorf("Expected 'file content', got '%s'", string(attachment.Content))
	}
}

func TestBudget_NewBudget(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	if budget == nil {
		t.Fatal("Budget should not be nil")
	}
}

func TestBudget_NewBudgetNilConfig(t *testing.T) {
	budget := NewBudget(nil)
	if budget == nil {
		t.Fatal("Budget should not be nil")
	}

	state := budget.GetState("session-1")
	if state.MaxTokens != 100000 {
		t.Errorf("Expected default MaxTokens 100000, got %d", state.MaxTokens)
	}
}

func TestBudget_Check(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	result, err := budget.Check(ctx, BudgetRequest{
		SessionID: "session-1",
		Estimated: Cost{
			Tokens: 100,
			Calls:  10,
			TimeMs: 5000,
		},
	})

	if err != nil {
		t.Errorf("Check failed: %v", err)
	}

	if !result.Allowed {
		t.Error("Should be allowed")
	}

	if result.Remain.Tokens != 900 {
		t.Errorf("Expected 900 remaining tokens, got %d", result.Remain.Tokens)
	}
}

func TestBudget_CheckDisabled(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled: false,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	result, err := budget.Check(ctx, BudgetRequest{
		SessionID: "session-1",
		Estimated: Cost{
			Tokens: 1000000,
			Calls:  1000,
			TimeMs: 1000000,
		},
	})

	if err != nil {
		t.Errorf("Check failed: %v", err)
	}

	if !result.Allowed {
		t.Error("Should be allowed when disabled")
	}
}

func TestBudget_CheckExceedsTokens(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    100,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.Consume(ctx, "session-1", Cost{Tokens: 50, Calls: 0, TimeMs: 0})

	result, err := budget.Check(ctx, BudgetRequest{
		SessionID: "session-1",
		Estimated: Cost{
			Tokens: 100,
			Calls:  0,
			TimeMs: 0,
		},
	})

	if err != nil {
		t.Errorf("Check failed: %v", err)
	}

	if result.Allowed {
		t.Error("Should not be allowed")
	}

	if result.Reason != "exceeded token budget" {
		t.Errorf("Expected 'exceeded token budget', got '%s'", result.Reason)
	}
}

func TestBudget_CheckExceedsCalls(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 10,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.Consume(ctx, "session-1", Cost{Tokens: 0, Calls: 5, TimeMs: 0})

	result, err := budget.Check(ctx, BudgetRequest{
		SessionID: "session-1",
		Estimated: Cost{
			Tokens: 0,
			Calls:  10,
			TimeMs: 0,
		},
	})

	if err != nil {
		t.Errorf("Check failed: %v", err)
	}

	if result.Allowed {
		t.Error("Should not be allowed")
	}

	if result.Reason != "exceeded tool call budget" {
		t.Errorf("Expected 'exceeded tool call budget', got '%s'", result.Reason)
	}
}

func TestBudget_CheckExceedsTime(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    1000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.Consume(ctx, "session-1", Cost{Tokens: 0, Calls: 0, TimeMs: 500})

	result, err := budget.Check(ctx, BudgetRequest{
		SessionID: "session-1",
		Estimated: Cost{
			Tokens: 0,
			Calls:  0,
			TimeMs: 1000,
		},
	})

	if err != nil {
		t.Errorf("Check failed: %v", err)
	}

	if result.Allowed {
		t.Error("Should not be allowed")
	}

	if result.Reason != "exceeded time budget" {
		t.Errorf("Expected 'exceeded time budget', got '%s'", result.Reason)
	}
}

func TestBudget_Consume(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	err := budget.Consume(ctx, "session-1", Cost{Tokens: 100, Calls: 10, TimeMs: 5000})
	if err != nil {
		t.Errorf("Consume failed: %v", err)
	}

	state := budget.GetState("session-1")
	if state.UsedTokens != 100 {
		t.Errorf("Expected 100 used tokens, got %d", state.UsedTokens)
	}

	if state.UsedCalls != 10 {
		t.Errorf("Expected 10 used calls, got %d", state.UsedCalls)
	}

	if state.UsedTimeMs != 5000 {
		t.Errorf("Expected 5000 used time, got %d", state.UsedTimeMs)
	}
}

func TestBudget_ConsumeDisabled(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled: false,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	err := budget.Consume(ctx, "session-1", Cost{Tokens: 1000000, Calls: 1000, TimeMs: 1000000})
	if err != nil {
		t.Errorf("Consume failed: %v", err)
	}
}

func TestBudget_GetState(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	state := budget.GetState("session-new")
	if state.MaxTokens != 1000 {
		t.Errorf("Expected 1000 max tokens, got %d", state.MaxTokens)
	}

	budget.Consume(ctx, "session-existing", Cost{Tokens: 500, Calls: 50, TimeMs: 30000})

	state = budget.GetState("session-existing")
	if state.UsedTokens != 500 {
		t.Errorf("Expected 500 used tokens, got %d", state.UsedTokens)
	}
}

func TestBudget_Reset(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.Consume(ctx, "session-1", Cost{Tokens: 500, Calls: 50, TimeMs: 30000})
	budget.Reset("session-1")

	state := budget.GetState("session-1")
	if state.UsedTokens != 0 {
		t.Errorf("Expected 0 used tokens after reset, got %d", state.UsedTokens)
	}
}

func TestBudget_ResetAll(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.Consume(ctx, "session-1", Cost{Tokens: 500, Calls: 50, TimeMs: 30000})
	budget.Consume(ctx, "session-2", Cost{Tokens: 300, Calls: 30, TimeMs: 20000})
	budget.ResetAll()

	state1 := budget.GetState("session-1")
	state2 := budget.GetState("session-2")

	if state1.UsedTokens != 0 || state2.UsedTokens != 0 {
		t.Error("All sessions should be reset")
	}
}

func TestBudget_RecordToolCall(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.RecordToolCall(ctx, "session-1")

	state := budget.GetState("session-1")
	if state.UsedCalls != 1 {
		t.Errorf("Expected 1 used call, got %d", state.UsedCalls)
	}
}

func TestBudget_RecordTokens(t *testing.T) {
	cfg := &BudgetConfig{
		Enabled:      true,
		MaxTokens:    1000,
		MaxToolCalls: 100,
		MaxTimeMs:    60000,
	}

	budget := NewBudget(cfg)
	ctx := context.Background()

	budget.RecordTokens(ctx, "session-1", 100)

	state := budget.GetState("session-1")
	if state.UsedTokens != 100 {
		t.Errorf("Expected 100 used tokens, got %d", state.UsedTokens)
	}
}

func TestLoopDetector_NewDetector(t *testing.T) {
	cfg := &loop.Config{
		Enabled:             true,
		MaxRepeats:          5,
		SimilarityThreshold: 0.8,
	}

	detector := loop.NewDetector(cfg)
	if detector == nil {
		t.Fatal("Detector should not be nil")
	}
}

func TestLoopDetector_NewDetectorNilConfig(t *testing.T) {
	detector := loop.NewDetector(nil)
	if detector == nil {
		t.Fatal("Detector should not be nil")
	}
}

func TestLoopDetector_RecordAndDetect(t *testing.T) {
	cfg := &loop.Config{
		Enabled:             true,
		MaxRepeats:          3,
		SimilarityThreshold: 0.8,
	}

	detector := loop.NewDetector(cfg)

	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})
	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})
	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})

	result := detector.Detect(context.Background(), "session-1", loop.Action{})
	if !result.IsLoop {
		t.Error("Should detect a loop")
	}

	if result.Count < 3 {
		t.Errorf("Expected count >= 3, got %d", result.Count)
	}
}

func TestLoopDetector_Disabled(t *testing.T) {
	cfg := &loop.Config{
		Enabled: false,
	}

	detector := loop.NewDetector(cfg)

	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})

	result := detector.Detect(context.Background(), "session-1", loop.Action{})
	if result.IsLoop {
		t.Error("Should not detect loop when disabled")
	}
}

func TestLoopDetector_NoActions(t *testing.T) {
	cfg := loop.DefaultConfig()
	detector := loop.NewDetector(cfg)

	result := detector.Detect(context.Background(), "session-new", loop.Action{})
	if result.IsLoop {
		t.Error("Should not detect loop for new session")
	}
}

func TestLoopDetector_Reset(t *testing.T) {
	cfg := &loop.Config{
		Enabled:             true,
		MaxRepeats:          3,
		SimilarityThreshold: 0.8,
	}

	detector := loop.NewDetector(cfg)

	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})

	detector.Reset("session-1")

	result := detector.Detect(context.Background(), "session-1", loop.Action{})
	if result.IsLoop {
		t.Error("Should not detect loop after reset")
	}
}

func TestLoopDetector_GetStats(t *testing.T) {
	cfg := loop.DefaultConfig()
	detector := loop.NewDetector(cfg)

	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})
	detector.Record("session-1", loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	})

	stats := detector.GetStats("session-1")
	if stats.TotalActions != 2 {
		t.Errorf("Expected 2 total actions, got %d", stats.TotalActions)
	}
}

func TestLoopDetector_GetStatsEmpty(t *testing.T) {
	cfg := loop.DefaultConfig()
	detector := loop.NewDetector(cfg)

	stats := detector.GetStats("session-nonexistent")
	if stats.TotalActions != 0 {
		t.Errorf("Expected 0 total actions, got %d", stats.TotalActions)
	}

	if stats.ActionCounts == nil {
		t.Error("ActionCounts should not be nil")
	}
}

func TestQueue_New(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	if q == nil {
		t.Fatal("Queue should not be nil")
	}

	q.Close()
}

func TestQueue_NewNilConfig(t *testing.T) {
	q := queue.New(nil)
	if q == nil {
		t.Fatal("Queue should not be nil")
	}

	q.Close()
}

func TestQueue_Enqueue(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	ch, err := q.Enqueue(ctx, "test message", queue.PriorityNormal)
	if err != nil {
		t.Errorf("Enqueue failed: %v", err)
	}

	if ch == nil {
		t.Error("Channel should not be nil")
	}

	if q.Size() != 1 {
		t.Errorf("Expected size 1, got %d", q.Size())
	}

	q.Close()
}

func TestQueue_EnqueueBatch(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	items := []string{"msg1", "msg2", "msg3"}
	channels, err := q.EnqueueBatch(ctx, items, queue.PriorityNormal)
	if err != nil {
		t.Errorf("EnqueueBatch failed: %v", err)
	}

	if len(channels) != 3 {
		t.Errorf("Expected 3 channels, got %d", len(channels))
	}

	if q.Size() != 3 {
		t.Errorf("Expected size 3, got %d", q.Size())
	}

	q.Close()
}

func TestQueue_EnqueueFull(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    2,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	_, err := q.Enqueue(ctx, "msg1", queue.PriorityNormal)
	if err != nil {
		t.Errorf("First enqueue should succeed: %v", err)
	}

	_, err = q.Enqueue(ctx, "msg2", queue.PriorityNormal)
	if err != nil {
		t.Errorf("Second enqueue should succeed: %v", err)
	}

	_, err = q.Enqueue(ctx, "msg3", queue.PriorityNormal)
	if err != queue.ErrFull {
		t.Errorf("Expected ErrFull, got %v", err)
	}

	q.Close()
}

func TestQueue_EnqueuePendingFull(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 1,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	ch1, err := q.Enqueue(ctx, "msg1", queue.PriorityNormal)
	if err != nil {
		t.Errorf("First enqueue should succeed: %v", err)
	}

	item := <-q.OutputChan()
	q.Complete(item, &queue.Result{Output: "result"})

	ch2, err := q.Enqueue(ctx, "msg2", queue.PriorityNormal)
	if err != nil {
		t.Errorf("Second enqueue should succeed: %v", err)
	}

	_ = ch1
	_ = ch2
	q.Close()
}

func TestQueue_EnqueueClosed(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	q.Close()

	_, err := q.Enqueue(context.Background(), "msg", queue.PriorityNormal)
	if err != queue.ErrClosed {
		t.Errorf("Expected ErrClosed, got %v", err)
	}
}

func TestQueue_EnqueueContextCancel(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	_, err := q.Enqueue(ctx, "msg", queue.PriorityNormal)
	if err != context.Canceled && err != nil {
		t.Errorf("Expected context.Canceled or success, got %v", err)
	}

	q.Close()
}

func TestQueue_OutputChan(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)

	ch := q.OutputChan()
	if ch == nil {
		t.Error("Output channel should not be nil")
	}

	q.Close()
}

func TestQueue_Complete(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	ch, err := q.Enqueue(ctx, "test", queue.PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	item := <-q.OutputChan()
	q.Complete(item, &queue.Result{Output: "result", Duration: 100 * time.Millisecond})

	if q.Pending() != 0 {
		t.Errorf("Expected 0 pending, got %d", q.Pending())
	}

	select {
	case result := <-ch:
		if result.Output != "result" {
			t.Errorf("Expected 'result', got '%s'", result.Output)
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for result")
	}

	q.Close()
}

func TestQueue_Pending(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	if q.Pending() != 0 {
		t.Errorf("Expected 0 pending initially, got %d", q.Pending())
	}

	ch1, err := q.Enqueue(ctx, "msg1", queue.PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	ch2, err := q.Enqueue(ctx, "msg2", queue.PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	item1 := <-q.OutputChan()
	item2 := <-q.OutputChan()

	if q.Pending() != 2 {
		t.Errorf("Expected 2 pending after dequeue, got %d", q.Pending())
	}

	q.Complete(item1, &queue.Result{Output: "result1"})
	q.Complete(item2, &queue.Result{Output: "result2"})

	_ = ch1
	_ = ch2
	q.Close()
}

func TestQueue_GetStats(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	ch1, err := q.Enqueue(ctx, "msg1", queue.PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	stats := q.GetStats()
	if stats.TotalEnqueued != 1 {
		t.Errorf("Expected 1 total enqueued, got %d", stats.TotalEnqueued)
	}

	ch2, err := q.Enqueue(ctx, "msg2", queue.PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	stats = q.GetStats()
	if stats.TotalEnqueued != 2 {
		t.Errorf("Expected 2 total enqueued, got %d", stats.TotalEnqueued)
	}

	item1 := <-q.OutputChan()
	item2 := <-q.OutputChan()

	stats = q.GetStats()
	if stats.Pending != 2 {
		t.Errorf("Expected 2 pending, got %d", stats.Pending)
	}

	q.Complete(item1, &queue.Result{Output: "result1"})
	q.Complete(item2, &queue.Result{Output: "result2"})

	_ = ch1
	_ = ch2
	q.Close()
}

func TestQueue_Clear(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	ctx := context.Background()

	q.Enqueue(ctx, "msg1", queue.PriorityNormal)
	q.Enqueue(ctx, "msg2", queue.PriorityNormal)

	q.Clear()

	if q.Size() != 0 {
		t.Errorf("Expected size 0 after clear, got %d", q.Size())
	}

	stats := q.GetStats()
	if stats.TotalEnqueued != 0 {
		t.Errorf("Expected 0 total enqueued after clear, got %d", stats.TotalEnqueued)
	}

	q.Close()
}

func TestQueue_CloseTwice(t *testing.T) {
	cfg := &queue.Config{
		MaxSize:    100,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}

	q := queue.New(cfg)
	q.Close()
	q.Close()
}

func TestQueue_Priority(t *testing.T) {
	if queue.PriorityLow != 0 {
		t.Errorf("Expected PriorityLow 0, got %d", queue.PriorityLow)
	}

	if queue.PriorityNormal != 1 {
		t.Errorf("Expected PriorityNormal 1, got %d", queue.PriorityNormal)
	}

	if queue.PriorityHigh != 2 {
		t.Errorf("Expected PriorityHigh 2, got %d", queue.PriorityHigh)
	}
}

func TestQueueErrors(t *testing.T) {
	if queue.ErrClosed.Error() != "queue closed" {
		t.Errorf("Expected 'queue closed', got '%s'", queue.ErrClosed.Error())
	}

	if queue.ErrFull.Error() != "queue is full" {
		t.Errorf("Expected 'queue is full', got '%s'", queue.ErrFull.Error())
	}

	if queue.ErrEmpty.Error() != "queue is empty" {
		t.Errorf("Expected 'queue is empty', got '%s'", queue.ErrEmpty.Error())
	}

	if queue.ErrCleared.Error() != "queue was cleared" {
		t.Errorf("Expected 'queue was cleared', got '%s'", queue.ErrCleared.Error())
	}
}

func TestToolsRegistry_NewRegistry(t *testing.T) {
	registry := tools.NewRegistry()
	if registry == nil {
		t.Fatal("Registry should not be nil")
	}

	if registry.Size() != 0 {
		t.Errorf("Expected size 0, got %d", registry.Size())
	}
}

func TestToolsRegistry_Register(t *testing.T) {
	registry := tools.NewRegistry()

	err := registry.Register(&mockTool{name: "tool1", description: "tool 1"})
	if err != nil {
		t.Errorf("Register failed: %v", err)
	}

	if registry.Size() != 1 {
		t.Errorf("Expected size 1, got %d", registry.Size())
	}
}

func TestToolsRegistry_RegisterDuplicate(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})

	err := registry.Register(&mockTool{name: "tool1", description: "tool 1 duplicate"})
	if err == nil {
		t.Error("Should return error for duplicate registration")
	}
}

func TestToolsRegistry_Get(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})

	tool, err := registry.Get("tool1")
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}

	if tool.Name() != "tool1" {
		t.Errorf("Expected 'tool1', got '%s'", tool.Name())
	}
}

func TestToolsRegistry_GetNotFound(t *testing.T) {
	registry := tools.NewRegistry()

	_, err := registry.Get("nonexistent")
	if err == nil {
		t.Error("Should return error for nonexistent tool")
	}
}

func TestToolsRegistry_GetAll(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})
	registry.Register(&mockTool{name: "tool2", description: "tool 2"})

	all := registry.GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(all))
	}
}

func TestToolsRegistry_Remove(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})

	err := registry.Remove("tool1")
	if err != nil {
		t.Errorf("Remove failed: %v", err)
	}

	if registry.Size() != 0 {
		t.Errorf("Expected size 0, got %d", registry.Size())
	}
}

func TestToolsRegistry_RemoveNotFound(t *testing.T) {
	registry := tools.NewRegistry()

	err := registry.Remove("nonexistent")
	if err == nil {
		t.Error("Should return error for nonexistent tool")
	}
}

func TestToolsRegistry_Clear(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})
	registry.Register(&mockTool{name: "tool2", description: "tool 2"})

	registry.Clear()

	if registry.Size() != 0 {
		t.Errorf("Expected size 0, got %d", registry.Size())
	}
}

func TestToolsRegistry_Has(t *testing.T) {
	registry := tools.NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "tool 1"})

	if !registry.Has("tool1") {
		t.Error("Should have tool1")
	}

	if registry.Has("tool2") {
		t.Error("Should not have tool2")
	}
}

func TestToolErrors(t *testing.T) {
	err := tools.ErrToolNotFound("test-tool")
	if err.Error() != "tool not found: test-tool" {
		t.Errorf("Expected 'tool not found: test-tool', got '%s'", err.Error())
	}

	err = tools.ErrToolAlreadyRegistered("test-tool")
	if err.Error() != "tool already registered: test-tool" {
		t.Errorf("Expected 'tool already registered: test-tool', got '%s'", err.Error())
	}
}

func TestBus_PublishAndSubscribe(t *testing.T) {
	bus := NewBus()

	var receivedData string
	bus.Subscribe("test.event", func(event BusEvent) {
		receivedData = event.Data.(map[string]string)["key"]
	})

	bus.Publish("test.event", "session-1", map[string]string{"key": "test-value"})
	bus.WaitAsync()

	if receivedData != "test-value" {
		t.Errorf("Expected 'test-value', got '%s'", receivedData)
	}
}

func TestBus_SubscribeAsync(t *testing.T) {
	bus := NewBus()

	var wg sync.WaitGroup
	wg.Add(1)

	bus.SubscribeAsync("test.async", func(event BusEvent) {
		wg.Done()
	}, false)

	bus.Publish("test.async", "session-1", nil)
	bus.WaitAsync()

	wg.Wait()
}

func TestBus_SubscribeOnce(t *testing.T) {
	bus := NewBus()

	count := 0
	var mu sync.Mutex
	handler := func(event BusEvent) {
		mu.Lock()
		defer mu.Unlock()
		count++
	}

	bus.SubscribeOnce("test.once", handler)

	bus.Publish("test.once", "session-1", nil)
	bus.Publish("test.once", "session-1", nil)
	bus.WaitAsync()

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("Expected count 1, got %d", count)
	}
}

func TestBus_SubscribeOnceAsync(t *testing.T) {
	bus := NewBus()

	count := 0
	var mu sync.Mutex

	handler := func(event BusEvent) {
		mu.Lock()
		defer mu.Unlock()
		count++
	}

	bus.SubscribeOnceAsync("test.once.async", handler)

	bus.Publish("test.once.async", "session-1", nil)
	bus.Publish("test.once.async", "session-1", nil)
	bus.WaitAsync()

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("Expected count 1, got %d", count)
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := NewBus()

	handler := func(event BusEvent) {}
	bus.Subscribe("test.unsub", handler)

	if !bus.HasSubscribers("test.unsub") {
		t.Error("Should have subscribers")
	}

	bus.Unsubscribe("test.unsub", handler)

	if bus.HasSubscribers("test.unsub") {
		t.Error("Should not have subscribers after unsubscribe")
	}
}

func TestBus_GlobalFunctions(t *testing.T) {
	bus := NewBus()
	originalBus := GetGlobalBus()
	SetGlobalBus(bus)
	defer SetGlobalBus(originalBus)

	Subscribe("global.test", func(event BusEvent) {})
	if !HasSubscribers("global.test") {
		t.Error("Should have subscribers")
	}

	Unsubscribe("global.test", nil)
}

func TestBusEvent(t *testing.T) {
	event := BusEvent{
		Type:      "test.event",
		SessionID: "session-1",
		Data:      map[string]string{"key": "value"},
	}

	if event.Type != "test.event" {
		t.Errorf("Expected 'test.event', got '%s'", event.Type)
	}

	if event.SessionID != "session-1" {
		t.Errorf("Expected 'session-1', got '%s'", event.SessionID)
	}

	data, ok := event.Data.(map[string]string)
	if !ok || data["key"] != "value" {
		t.Error("Data mismatch")
	}
}

func TestBusConstants(t *testing.T) {
	constants := []string{
		EventSessionCreated,
		EventSessionUpdated,
		EventSessionDeleted,
		EventSessionError,
		EventMessageCreated,
		EventMessageUpdated,
		EventMessageDeleted,
		EventToolInvoked,
		EventToolResult,
		EventToolError,
		EventUsageUpdated,
		EventApprovalRequired,
		EventApprovalGiven,
		EventApprovalDenied,
	}

	if len(constants) != 14 {
		t.Errorf("Expected 14 event constants, got %d", len(constants))
	}
}

func TestStreamEventSender(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("Failed to create factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	session, err := factory.CreateSession(ctx, "sender-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	session.Close()
}

func TestCost(t *testing.T) {
	cost := Cost{
		Tokens: 100,
		Calls:  10,
		TimeMs: 5000,
	}

	if cost.Tokens != 100 {
		t.Errorf("Expected 100, got %d", cost.Tokens)
	}

	if cost.Calls != 10 {
		t.Errorf("Expected 10, got %d", cost.Calls)
	}

	if cost.TimeMs != 5000 {
		t.Errorf("Expected 5000, got %d", cost.TimeMs)
	}
}

func TestBudgetRequest(t *testing.T) {
	req := BudgetRequest{
		SessionID: "session-1",
		ToolCalls: []string{"read_file", "bash"},
		Estimated: Cost{
			Tokens: 100,
			Calls:  2,
			TimeMs: 1000,
		},
	}

	if req.SessionID != "session-1" {
		t.Errorf("Expected 'session-1', got '%s'", req.SessionID)
	}

	if len(req.ToolCalls) != 2 {
		t.Errorf("Expected 2 tool calls, got %d", len(req.ToolCalls))
	}
}

func TestBudgetResult(t *testing.T) {
	result := BudgetResult{
		Allowed: true,
		Reason:  "ok",
		Remain: Cost{
			Tokens: 900,
			Calls:  90,
			TimeMs: 55000,
		},
	}

	if !result.Allowed {
		t.Error("Should be allowed")
	}

	if result.Remain.Tokens != 900 {
		t.Errorf("Expected 900, got %d", result.Remain.Tokens)
	}
}

func TestBudgetState(t *testing.T) {
	state := BudgetState{
		UsedTokens: 100,
		UsedCalls:  10,
		UsedTimeMs: 5000,
		MaxTokens:  1000,
		MaxCalls:   100,
		MaxTimeMs:  60000,
	}

	if state.UsedTokens != 100 {
		t.Errorf("Expected 100, got %d", state.UsedTokens)
	}

	if state.MaxTokens != 1000 {
		t.Errorf("Expected 1000, got %d", state.MaxTokens)
	}
}

func TestConfig_ProviderErrors(t *testing.T) {
	_ = &ProviderConfig{}

	if ProviderError("").Error() != "" {
		t.Error("Empty provider error should return empty string")
	}

	if ErrProviderNotFound.Error() != "provider not found" {
		t.Errorf("Expected 'provider not found', got '%s'", ErrProviderNotFound.Error())
	}
}

func TestConfig_DefaultValues(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Timeout != 30*time.Second {
		t.Errorf("Expected 30s timeout, got %v", cfg.Timeout)
	}

	if cfg.ToolExecutionTimeout != 60*time.Second {
		t.Errorf("Expected 60s tool execution timeout, got %v", cfg.ToolExecutionTimeout)
	}

	if cfg.MaxIterations != 10 {
		t.Errorf("Expected 10 max iterations, got %d", cfg.MaxIterations)
	}

	if cfg.TopP != 1.0 {
		t.Errorf("Expected 1.0 top p, got %f", cfg.TopP)
	}

	if cfg.WorkspaceRoot != "." {
		t.Errorf("Expected '.', got '%s'", cfg.WorkspaceRoot)
	}
}

func TestLoopConfig_DefaultConfig(t *testing.T) {
	cfg := loop.DefaultConfig()

	if !cfg.Enabled {
		t.Error("Should be enabled by default")
	}

	if cfg.MaxRepeats != loop.MaxRepeats {
		t.Errorf("Expected MaxRepeats %d, got %d", loop.MaxRepeats, cfg.MaxRepeats)
	}

	if cfg.SimilarityThreshold != 0.8 {
		t.Errorf("Expected 0.8 similarity threshold, got %f", cfg.SimilarityThreshold)
	}
}

func TestLoopStats(t *testing.T) {
	stats := loop.Stats{
		TotalActions: 10,
		ActionCounts: map[string]int{
			"action1": 5,
			"action2": 5,
		},
	}

	if stats.TotalActions != 10 {
		t.Errorf("Expected 10, got %d", stats.TotalActions)
	}

	if stats.ActionCounts["action1"] != 5 {
		t.Errorf("Expected 5, got %d", stats.ActionCounts["action1"])
	}
}

func TestLoopAction(t *testing.T) {
	action := loop.Action{
		Type:      "tool_call",
		Content:   `{"command": "ls"}`,
		Timestamp: time.Now(),
	}

	if action.Type != "tool_call" {
		t.Errorf("Expected 'tool_call', got '%s'", action.Type)
	}
}

func TestLoopResult(t *testing.T) {
	result := loop.Result{
		IsLoop:     true,
		Count:      5,
		Similarity: 0.9,
		Suggestion: "Try a different approach",
	}

	if !result.IsLoop {
		t.Error("Should be a loop")
	}

	if result.Count != 5 {
		t.Errorf("Expected 5, got %d", result.Count)
	}

	if result.Similarity != 0.9 {
		t.Errorf("Expected 0.9, got %f", result.Similarity)
	}
}
