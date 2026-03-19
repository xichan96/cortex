package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

type subagentMockLLMProvider struct {
	mu           sync.Mutex
	responses    []string
	responseIdx  int
	callCount    int
	shouldStream bool
}

func newSubagentMockLLMProvider(responses []string) *subagentMockLLMProvider {
	return &subagentMockLLMProvider{
		responses:    responses,
		responseIdx:  0,
		callCount:    0,
		shouldStream: false,
	}
}

func (m *subagentMockLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
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

func (m *subagentMockLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	ch <- types.StreamMessage{
		Type:    "chunk",
		Content: m.responses[0],
	}
	close(ch)
	return ch, nil
}

func (m *subagentMockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.Chat(ctx, messages)
}

func (m *subagentMockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	return m.ChatStream(ctx, messages)
}

func (m *subagentMockLLMProvider) GetModelName() string {
	return "mock-model"
}

func (m *subagentMockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

type subagentMockFactory struct{}

func (f *subagentMockFactory) GetLLMProvider() types.LLMProvider {
	return newSubagentMockLLMProvider([]string{"mock response"})
}

func (f *subagentMockFactory) GetTools() []types.Tool {
	return []types.Tool{}
}

func (f *subagentMockFactory) GetAgent(name string) (*Info, bool) {
	info, exists := DefaultAgents()[name]
	return info, exists
}

type mockEventEmitter struct {
	events []SubagentEvent
}

func (e *mockEventEmitter) Emit(event *SubagentEvent) {
	e.events = append(e.events, *event)
}

func (e *mockEventEmitter) GetEvents() []SubagentEvent {
	return e.events
}

func TestShouldDelegate_KeywordMatch(t *testing.T) {
	config := &SubagentConfig{
		Enabled:          true,
		TriggerOnKeyword: true,
		Triggers: []SubagentTrigger{
			{
				AgentName: "general",
				Keywords:  []string{"research", "analyze"},
				Priority:  5,
			},
		},
	}

	manager := NewSubagentManager(config, &subagentMockFactory{})
	if manager == nil {
		t.Fatal("expected manager to be created")
	}

	tests := []struct {
		name      string
		input     string
		wantAgent string
	}{
		{"research keyword", "research about the codebase", "general"},
		{"analyze keyword", "analyze the code quality", "general"},
		{"no match", "just a simple greeting", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.ShouldDelegate(tt.input)
			if tt.wantAgent == "" {
				if result != nil {
					t.Errorf("expected no delegation, got %s", result.AgentName)
				}
			} else {
				if result == nil {
					t.Errorf("expected delegation to %s, got nil", tt.wantAgent)
				} else if result.AgentName != tt.wantAgent {
					t.Errorf("expected agent %s, got %s", tt.wantAgent, result.AgentName)
				}
			}
		})
	}

	manager.Close()
}

func TestShouldDelegate_Disabled(t *testing.T) {
	config := &SubagentConfig{
		Enabled: false,
	}

	manager := NewSubagentManager(config, &subagentMockFactory{})
	if manager != nil {
		t.Error("expected nil manager when disabled")
	}
}

func TestShouldDelegate_NilConfig(t *testing.T) {
	manager := NewSubagentManager(nil, &subagentMockFactory{})
	if manager != nil {
		t.Error("expected nil manager for nil config")
	}
}

func TestSubagentHandler_ProcessInput(t *testing.T) {
	config := &SubagentConfig{
		Enabled:          true,
		TriggerOnKeyword: true,
		Triggers: []SubagentTrigger{
			{
				AgentName: "general",
				Keywords:  []string{"research"},
				Priority:  5,
			},
		},
	}

	manager := NewSubagentManager(config, &subagentMockFactory{})
	if manager == nil {
		t.Fatal("expected manager to be created")
	}

	handler := NewSubagentHandler(manager, nil)
	if handler == nil {
		t.Fatal("expected handler to be created")
	}

	handled, err := handler.ProcessInput(nil, "research about files")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected input to be handled")
	}

	handled, err = handler.ProcessInput(nil, "hello")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected input not to be handled")
	}

	manager.Close()
}
