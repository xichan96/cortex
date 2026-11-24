package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// fakeCompactMemory is a types.MemoryProvider with deterministic compression:
// when more than two messages are stored it folds everything except the last
// two into a fixed summary.
type fakeCompactMemory struct {
	mu      sync.Mutex
	msgs    []types.Message
	summary string
	usedLLM bool
	calls   int
	keep    int
}

func (f *fakeCompactMemory) LoadMemoryVariables(context.Context) (map[string]interface{}, error) {
	return nil, nil
}

func (f *fakeCompactMemory) SaveContext(context.Context, map[string]interface{}, map[string]interface{}) error {
	return nil
}

func (f *fakeCompactMemory) Clear(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = nil
	f.summary = ""
	f.usedLLM = false
	return nil
}

func (f *fakeCompactMemory) GetChatHistory(context.Context) ([]types.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.Message(nil), f.msgs...), nil
}

func (f *fakeCompactMemory) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.keep = maxMessages
	if len(f.msgs) <= 2 {
		return nil
	}
	f.summary = "summary"
	f.usedLLM = true
	f.msgs = f.msgs[len(f.msgs)-2:]
	return nil
}

func (f *fakeCompactMemory) StoredMessageCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs), nil
}

func (f *fakeCompactMemory) LastCompactUsedLLM() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usedLLM
}

func (f *fakeCompactMemory) CompactSummary(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.summary, nil
}

func newMessages(n int) []types.Message {
	msgs := make([]types.Message, n)
	for i := range msgs {
		msgs[i] = types.Message{Role: "user", Content: "m"}
	}
	return msgs
}

func TestCompactMemory_Stats(t *testing.T) {
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	mem := &fakeCompactMemory{msgs: newMessages(5)}
	ae.SetMemory(context.Background(), mem)

	res, err := ae.CompactMemory(context.Background())
	if err != nil {
		t.Fatalf("CompactMemory: %v", err)
	}

	if res.Compacted != 3 {
		t.Fatalf("Compacted = %d, want 3", res.Compacted)
	}
	if res.Kept != 2 {
		t.Fatalf("Kept = %d, want 2", res.Kept)
	}
	if res.SummaryLen != len("summary") {
		t.Fatalf("SummaryLen = %d, want %d", res.SummaryLen, len("summary"))
	}
	if !res.UsedLLM {
		t.Fatal("UsedLLM should be reported from the provider")
	}
	if mem.calls != 1 {
		t.Fatalf("CompressMemory calls = %d, want 1", mem.calls)
	}
	if mem.keep != types.NewAgentConfig().MemoryCompressThreshold {
		t.Fatalf("keepWindow = %d, want %d", mem.keep, types.NewAgentConfig().MemoryCompressThreshold)
	}
}

func TestCompactMemory_NoOpWhenBelowRetention(t *testing.T) {
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	mem := &fakeCompactMemory{msgs: newMessages(2)}
	ae.SetMemory(context.Background(), mem)

	res, err := ae.CompactMemory(context.Background())
	if err != nil {
		t.Fatalf("CompactMemory: %v", err)
	}
	if res.Compacted != 0 {
		t.Fatalf("Compacted = %d, want 0 for a no-op", res.Compacted)
	}
	if res.Kept != 2 {
		t.Fatalf("Kept = %d, want 2", res.Kept)
	}
	if res.SummaryLen != 0 {
		t.Fatalf("SummaryLen = %d, want 0", res.SummaryLen)
	}
	if res.UsedLLM {
		t.Fatal("UsedLLM should stay false when nothing was compressed")
	}
	if mem.calls != 1 {
		t.Fatalf("provider should still be asked once, got %d calls", mem.calls)
	}
}

func TestCompactMemory_NoMemory(t *testing.T) {
	ae := NewAgentEngine(nil, types.NewAgentConfig())

	if _, err := ae.CompactMemory(context.Background()); !errors.Is(err, ErrMemoryNotConfigured) {
		t.Fatalf("err = %v, want ErrMemoryNotConfigured", err)
	}
}

// TestCompactMemory_SerializesWithAutoCompress holds the shared lock the way an
// in-flight automatic compression does and verifies CompactMemory waits for it.
func TestCompactMemory_SerializesWithAutoCompress(t *testing.T) {
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetMemory(context.Background(), &fakeCompactMemory{msgs: newMessages(5)})

	ae.memoryCompressMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := ae.CompactMemory(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		ae.memoryCompressMu.Unlock()
		t.Fatalf("CompactMemory returned while the compression lock was held (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	ae.memoryCompressMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CompactMemory: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CompactMemory did not proceed after the lock was released")
	}
}
