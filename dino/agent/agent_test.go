package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
)

type mockLLMProvider struct {
	response string
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: m.response}, nil
}

func (m *mockLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	ch <- types.StreamMessage{Type: "chunk", Content: m.response}
	close(ch)
	return ch, nil
}

func (m *mockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{Content: m.response}, nil
}

func (m *mockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	ch <- types.StreamMessage{Type: "chunk", Content: m.response}
	close(ch)
	return ch, nil
}

func (m *mockLLMProvider) GetModelName() string { return "mock-model" }

func (m *mockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

type mockTool struct {
	name        string
	description string
}

func (m *mockTool) Name() string                   { return m.name }
func (m *mockTool) Description() string            { return m.description }
func (m *mockTool) Schema() map[string]interface{} { return nil }
func (m *mockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return "executed", nil
}
func (m *mockTool) Metadata() types.ToolMetadata { return types.ToolMetadata{} }

type mockFactory struct {
	agents map[string]*Info
	tools  []types.Tool
	llm    types.LLMProvider
}

func (f *mockFactory) GetAgent(name string) (*Info, bool) {
	info, ok := f.agents[name]
	return info, ok
}

func (f *mockFactory) GetLLMProvider() types.LLMProvider { return f.llm }
func (f *mockFactory) GetTools() []types.Tool            { return f.tools }

func TestNewSubagent(t *testing.T) {
	info := &Info{
		Name:       "test",
		Mode:       ModeSubagent,
		Permission: permission.DefaultRuleset(),
	}
	tools := []types.Tool{&mockTool{name: "test_tool"}}

	sa, err := NewSubagent(info, &mockLLMProvider{response: "hello"}, tools)
	if err != nil {
		t.Fatalf("NewSubagent failed: %v", err)
	}
	if sa == nil {
		t.Fatal("NewSubagent returned nil")
	}
	sa.Close()
}

func TestNewSubagentInvalidMode(t *testing.T) {
	info := &Info{
		Name: "test",
		Mode: ModePrimary,
	}

	_, err := NewSubagent(info, &mockLLMProvider{response: "hello"}, nil)
	if err == nil {
		t.Fatal("expected error for non-subagent mode")
	}
}

func TestSubagentInterface(t *testing.T) {
	info := &Info{
		Name:       "test",
		Mode:       ModeSubagent,
		Permission: permission.DefaultRuleset(),
	}

	var sa Subagent
	var err error

	sa, err = NewSubagent(info, &mockLLMProvider{response: "hello"}, nil)
	if err != nil {
		t.Fatalf("NewSubagent failed: %v", err)
	}

	if sa == nil {
		t.Fatal("Subagent is nil")
	}

	sa.Close()
}

func TestSubagentResult(t *testing.T) {
	result := &Result{
		Output: "test output",
	}

	if result.Output != "test output" {
		t.Errorf("expected output 'test output', got %s", result.Output)
	}
}

func TestRequest(t *testing.T) {
	req := &Request{
		AgentName: "test",
		Prompt:    "test prompt",
		Input:     "test input",
		Files: []FileAttachment{
			{Path: "/test/file.go", Name: "file.go", Content: []byte("content")},
		},
	}

	if req.AgentName != "test" {
		t.Errorf("expected AgentName 'test', got %s", req.AgentName)
	}
	if len(req.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(req.Files))
	}
}

func TestFileAttachment(t *testing.T) {
	fa := FileAttachment{
		Path:    "/path/to/file",
		Name:    "file.txt",
		Content: []byte("hello"),
	}

	if fa.Path != "/path/to/file" {
		t.Errorf("expected Path '/path/to/file', got %s", fa.Path)
	}
	if string(fa.Content) != "hello" {
		t.Errorf("expected Content 'hello', got %s", string(fa.Content))
	}
}

func TestNewManager(t *testing.T) {
	factory := &mockFactory{
		agents: make(map[string]*Info),
		tools:  []types.Tool{},
		llm:    &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.subagents) != 0 {
		t.Errorf("expected empty subagents map, got %d", len(m.subagents))
	}
}

func TestManagerGetSubagentNotFound(t *testing.T) {
	factory := &mockFactory{
		agents: make(map[string]*Info),
		tools:  []types.Tool{},
		llm:    &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	_, err := m.GetSubagent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestManagerGetSubagentNotSubagentMode(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"primary": {
				Name: "primary",
				Mode: ModePrimary,
			},
		},
		tools: []types.Tool{},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	_, err := m.GetSubagent("primary")
	if err == nil {
		t.Fatal("expected error for non-subagent mode")
	}
}

func TestManagerGetSubagent(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{&mockTool{name: "test"}},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	sa, err := m.GetSubagent("general")
	if err != nil {
		t.Fatalf("GetSubagent failed: %v", err)
	}
	if sa == nil {
		t.Fatal("GetSubagent returned nil")
	}
}

func TestManagerGetSubagentCached(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{&mockTool{name: "test"}},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	sa1, err := m.GetSubagent("general")
	if err != nil {
		t.Fatalf("GetSubagent failed: %v", err)
	}

	sa2, err := m.GetSubagent("general")
	if err != nil {
		t.Fatalf("GetSubagent failed: %v", err)
	}

	if sa1 == nil || sa2 == nil {
		t.Fatal("subagents should not be nil")
	}

	if len(m.subagents) != 1 {
		t.Errorf("expected 1 cached subagent, got %d", len(m.subagents))
	}
}

func TestManagerExecute(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{},
		llm:   &mockLLMProvider{response: "test response"},
	}

	m := NewManager(factory)

	req := &Request{
		AgentName: "general",
		Input:     "hello",
	}

	result, err := m.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Output != "test response" {
		t.Errorf("expected 'test response', got %s", result.Output)
	}
}

func TestManagerExecuteNotFound(t *testing.T) {
	factory := &mockFactory{
		agents: make(map[string]*Info),
		tools:  []types.Tool{},
		llm:    &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	req := &Request{
		AgentName: "nonexistent",
	}

	_, err := m.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestManagerClose(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	_, _ = m.GetSubagent("general")

	if len(m.subagents) != 1 {
		t.Fatalf("expected 1 subagent, got %d", len(m.subagents))
	}

	m.Close()

	if len(m.subagents) != 0 {
		t.Errorf("expected 0 subagents after Close, got %d", len(m.subagents))
	}
}

func TestManagerCloseAgent(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
			"explore": {
				Name:       "explore",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	_, _ = m.GetSubagent("general")
	_, _ = m.GetSubagent("explore")

	if len(m.subagents) != 2 {
		t.Fatalf("expected 2 subagents, got %d", len(m.subagents))
	}

	m.CloseAgent("general")

	if len(m.subagents) != 1 {
		t.Errorf("expected 1 subagent after CloseAgent, got %d", len(m.subagents))
	}

	m.CloseAgent("nonexistent")
}

func TestManagerConcurrency(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {
				Name:       "general",
				Mode:       ModeSubagent,
				Permission: permission.DefaultRuleset(),
			},
		},
		tools: []types.Tool{},
		llm:   &mockLLMProvider{response: "test"},
	}

	m := NewManager(factory)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.GetSubagent("general")
		}()
	}
	wg.Wait()

	if len(m.subagents) != 1 {
		t.Errorf("expected 1 cached subagent with concurrency, got %d", len(m.subagents))
	}
}

func TestFactoryInterface(t *testing.T) {
	factory := &mockFactory{
		agents: map[string]*Info{
			"test": {Name: "test", Mode: ModeSubagent},
		},
		tools: []types.Tool{&mockTool{name: "tool1"}},
		llm:   &mockLLMProvider{response: "test"},
	}

	info, ok := factory.GetAgent("test")
	if !ok || info == nil {
		t.Fatal("GetAgent failed")
	}

	if factory.GetLLMProvider() == nil {
		t.Fatal("GetLLMProvider returned nil")
	}

	if len(factory.GetTools()) != 1 {
		t.Errorf("expected 1 tool, got %d", len(factory.GetTools()))
	}
}
