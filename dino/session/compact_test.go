package session

import (
	"context"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
)

// sessionFakeMemory compresses down to the last two messages.
type sessionFakeMemory struct {
	mu      sync.Mutex
	msgs    []types.Message
	summary string
}

func (f *sessionFakeMemory) LoadMemoryVariables(context.Context) (map[string]interface{}, error) {
	return nil, nil
}

func (f *sessionFakeMemory) SaveContext(context.Context, map[string]interface{}, map[string]interface{}) error {
	return nil
}

func (f *sessionFakeMemory) Clear(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = nil
	f.summary = ""
	return nil
}

func (f *sessionFakeMemory) GetChatHistory(context.Context) ([]types.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.Message(nil), f.msgs...), nil
}

func (f *sessionFakeMemory) CompressMemory(context.Context, types.LLMProvider, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) <= 2 {
		return nil
	}
	f.summary = "session summary"
	f.msgs = f.msgs[len(f.msgs)-2:]
	return nil
}

func (f *sessionFakeMemory) StoredMessageCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs), nil
}

func (f *sessionFakeMemory) GetSummary(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.summary, nil
}

func newTestSession(t *testing.T, agent *engine.AgentEngine) *Session {
	t.Helper()
	return NewSession("compact-test", agent, nil, context.Background(), DefaultConfig(), nil, nil, nil)
}

func TestSessionCompact_ForwardsToEngine(t *testing.T) {
	agent := engine.NewAgentEngine(nil, types.NewAgentConfig())
	mem := &sessionFakeMemory{msgs: []types.Message{
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "2"},
		{Role: "user", Content: "3"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "5"},
	}}
	agent.SetMemory(context.Background(), mem)
	sess := newTestSession(t, agent)

	res, err := sess.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.Compacted != 3 {
		t.Fatalf("Compacted = %d, want 3", res.Compacted)
	}
	if res.Kept != 2 {
		t.Fatalf("Kept = %d, want 2", res.Kept)
	}
	if res.SummaryLen != len("session summary") {
		t.Fatalf("SummaryLen = %d, want %d", res.SummaryLen, len("session summary"))
	}
}

func TestSessionCompact_NoAgent(t *testing.T) {
	sess := newTestSession(t, nil)
	if _, err := sess.Compact(context.Background()); err == nil {
		t.Fatal("expected an error when the session has no agent")
	}
}
