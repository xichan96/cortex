package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
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
	return m.ChatWithToolsStream(ctx, messages, nil)
}

// usage 返回每次调用固定的 mock token 用量（供结构化采集断言）。
func (m *subagentMockLLMProvider) usage() types.Usage {
	return types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
}

func (m *subagentMockLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.Chat(ctx, messages)
}

func (m *subagentMockLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	m.mu.Lock()
	m.callCount++
	if m.responseIdx >= len(m.responses) {
		m.responseIdx = 0
	}
	content := m.responses[m.responseIdx]
	m.responseIdx++
	m.mu.Unlock()

	ch := make(chan types.StreamMessage, 1)
	u := m.usage()
	ch <- types.StreamMessage{
		Type:    "chunk",
		Content: content,
		Usage:   &u,
	}
	close(ch)
	return ch, nil
}

// callCountSafe 返回累计调用次数（测试断言用，加锁读）。
func (m *subagentMockLLMProvider) callCountSafe() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *subagentMockLLMProvider) GetModelName() string {
	return "mock-model"
}

func (m *subagentMockLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-model"}
}

type subagentMockFactory struct {
	llm types.LLMProvider // 可选覆盖，nil 时用默认 mock
}

func (f *subagentMockFactory) GetLLMProvider() types.LLMProvider {
	if f != nil && f.llm != nil {
		return f.llm
	}
	return newSubagentMockLLMProvider([]string{"mock response"})
}

func (f *subagentMockFactory) GetTools() []types.Tool {
	return []types.Tool{}
}

func (f *subagentMockFactory) GetAgent(name string) (*Info, bool) {
	info, exists := DefaultAgents()[name]
	return info, exists
}

func (f *subagentMockFactory) GetParentRuleset() permission.Ruleset {
	// 测试默认不限制（返回空 ruleset，restrict-only 不生效）。
	return nil
}

func TestNewSubagentManager_Disabled(t *testing.T) {
	config := &SubagentConfig{
		Enabled: false,
	}

	manager := NewSubagentManager(config, &subagentMockFactory{})
	if manager != nil {
		t.Error("expected nil manager when disabled")
	}
}

func TestNewSubagentManager_NilConfig(t *testing.T) {
	manager := NewSubagentManager(nil, &subagentMockFactory{})
	if manager != nil {
		t.Error("expected nil manager for nil config")
	}
}
