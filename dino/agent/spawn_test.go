package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// TestSpawn_FireAndForget：Spawn 立即返回，Done channel 在完成后关闭，mailbox 有结果。
func TestSpawn_FireAndForget(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"spawned result"}))
	sm.SetMaxConcurrentSpawns(2)
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true)
	sm.SetNotifier(n)

	start := time.Now()
	handle, err := sm.Spawn(context.Background(), "sess-1", &Request{AgentName: "general", Input: "do it"},
		RootAgentPath(), mb, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	if handle.TaskID == "" {
		t.Fatal("expected non-empty task_id")
	}
	// 立即返回：不阻塞。
	if time.Since(start) > 2*time.Second {
		t.Errorf("Spawn should return immediately, took %v", time.Since(start))
	}

	select {
	case <-handle.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done channel not closed after completion")
	}

	if got := mb.Peek(handle.TaskID); got == nil {
		t.Fatal("expected mailbox to have result after completion")
	} else if got.Output != "spawned result" {
		t.Errorf("expected output 'spawned result', got %q", got.Output)
	}
	if got := mb.Peek(handle.TaskID).AgentPath; got != "/root/general" {
		t.Errorf("expected agent_path /root/general, got %q", got)
	}
}

// TestSpawn_ParentCancelKillsSubagent：父 ctx cancel → 子代理 ctx 取消，
// sessionCancels 分桶被清理。
func TestSpawn_ParentCancelKillsSubagent(t *testing.T) {
	// 用阻塞 mock：等子代理真正进入执行后再 cancel，确保 cancel 作用于运行中的子代理。
	blocker := newBlockingMockProvider([]string{"x"})
	sm := newTestSubagentManager(t, blocker)
	sm.SetMaxConcurrentSpawns(2)
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true)
	sm.SetNotifier(n)

	ctx, cancel := context.WithCancel(context.Background())
	handle, err := sm.Spawn(ctx, "sess-1", &Request{AgentName: "general", Input: "run"},
		RootAgentPath(), mb, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	blocker.waitStarted()
	cancel()

	select {
	case <-handle.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after parent cancel")
	}
	// 分桶应被 unregisterSubagentCancel 清理。
	sm.mu.RLock()
	bucket := sm.sessionCancels["sess-1"]
	sm.mu.RUnlock()
	if len(bucket) != 0 {
		t.Errorf("expected sessionCancels bucket empty after cancel, got %d", len(bucket))
	}
}

// TestSpawn_RegistersSessionCancel：registerSubagentCancel 写入分桶，CloseSession 后 cancel 全调。
func TestSpawn_RegistersSessionCancel(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"x"}))
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true)
	sm.SetNotifier(n)

	// 阻塞 LLM 让子代理挂起，便于观察 cancel。
	blocker := newBlockingMockProvider([]string{"done"})
	sm2 := newTestSubagentManager(t, blocker)
	sm2.SetMaxConcurrentSpawns(4)
	sm2.SetNotifier(n)

	handle, err := sm2.Spawn(context.Background(), "sess-c", &Request{AgentName: "general", Input: "run"},
		RootAgentPath(), mb, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	// 等子代理进入执行（阻塞在 mock）。
	blocker.waitStarted()

	// CloseSession → cancel 全调 → Done 关闭（子代理 ctx 取消，Execute 返回 cancelled）。
	sm2.CloseSession("sess-c")
	select {
	case <-handle.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after CloseSession")
	}
	sm2.mu.RLock()
	bucket := sm2.sessionCancels["sess-c"]
	sm2.mu.RUnlock()
	if len(bucket) != 0 {
		t.Errorf("expected sessionCancels cleaned after CloseSession, got %d", len(bucket))
	}
}

// TestSpawn_MaxConcurrent：semaphore 上限生效——并发运行的子代理 ≤ 上限。
func TestSpawn_MaxConcurrent(t *testing.T) {
	blocker := newBlockingMockProvider([]string{"r"})
	sm := newTestSubagentManager(t, blocker)
	sm.SetMaxConcurrentSpawns(2)
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true)
	sm.SetNotifier(n)

	// 发 5 个 spawn，全部立即返回。
	var handles []*SpawnHandle
	for i := 0; i < 5; i++ {
		h, err := sm.Spawn(context.Background(), "sess-m", &Request{AgentName: "general", Input: "t"},
			RootAgentPath(), mb, SpawnOptions{})
		if err != nil {
			t.Fatalf("Spawn %d failed: %v", i, err)
		}
		handles = append(handles, h)
	}
	// 等足够长时间让前 2 个进入执行、其余排队。
	blocker.waitCount(2)
	time.Sleep(50 * time.Millisecond)
	if n := blocker.running(); n > 2 {
		t.Errorf("expected at most 2 concurrent executions, got %d", n)
	}
	// 释放前 2 个，其余应陆续执行完毕。
	blocker.releaseAll()
	for i, h := range handles {
		select {
		case <-h.Done:
		case <-time.After(5 * time.Second):
			t.Fatalf("handle %d not done", i)
		}
	}
}

// TestCompletionNotifier_PutsMailbox：Notify 后 mailbox 有结果。
func TestCompletionNotifier_PutsMailbox(t *testing.T) {
	mb := NewMailbox(0, 0)
	events := 0
	n := NewCompletionNotifier(func(string) *Mailbox { return mb },
		func(eventType, sid string, data interface{}) { events++ }, true)
	env := testResult(DelegateStatusCompleted)
	n.Notify("sess-1", "t1", env)
	if got := mb.Peek("t1"); got != env {
		t.Errorf("expected mailbox to have result, got %v", got)
	}
	if events != 1 {
		t.Errorf("expected 1 event, got %d", events)
	}
}

// TestCompletionNotifier_Disabled：enabled=false 时不 Put 不发事件（OPTIONAL-1）。
func TestCompletionNotifier_Disabled(t *testing.T) {
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, false)
	n.Notify("sess-1", "t1", testResult("x"))
	if mb.Len() != 0 {
		t.Errorf("expected no Put when disabled, Len=%d", mb.Len())
	}
}

// TestCompletionNotifier_MailboxFullDegrades：cap 满 → Notify 不 panic、仍发事件。
func TestCompletionNotifier_MailboxFullDegrades(t *testing.T) {
	mb := NewMailbox(1, 0)
	events := 0
	n := NewCompletionNotifier(func(string) *Mailbox { return mb },
		func(eventType, sid string, data interface{}) { events++ }, true)
	n.Notify("sess-1", "t1", testResult("1"))
	if events != 1 {
		t.Errorf("expected 1 event after first, got %d", events)
	}
	n.Notify("sess-1", "t2", testResult("2"))
	if events != 2 {
		t.Errorf("expected event still fired on full mailbox, got %d", events)
	}
	// 不 panic，第一条仍在。
	if got := mb.Peek("t1"); got == nil {
		t.Error("expected t1 still present")
	}
}

// blockingMockProvider 阻塞在 ChatWithToolsStream，配合测试观察并发/取消。
// 实现 types.LLMProvider 接口（与 subagentMockLLMProvider 同签名）。
// 初始化即创建一个 wake channel（未 release 前所有调用都阻塞等它）。
type blockingMockProvider struct {
	mu        sync.Mutex
	responses []string
	started   int
	runningN  int
	released  bool
	wake      chan struct{}
}

func newBlockingMockProvider(responses []string) *blockingMockProvider {
	return &blockingMockProvider{
		responses: responses,
		wake:      make(chan struct{}),
	}
}

func (b *blockingMockProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{}, nil
}

func (b *blockingMockProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return b.ChatWithToolsStream(ctx, messages, nil)
}

func (b *blockingMockProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{}, nil
}

func (b *blockingMockProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	b.mu.Lock()
	b.started++
	b.runningN++
	wake := b.wake
	if b.released {
		wake = nil
	}
	b.mu.Unlock()

	if wake != nil {
		select {
		case <-wake:
		case <-ctx.Done():
		}
	}
	u := types.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	ch := make(chan types.StreamMessage, 1)
	content := "r"
	if len(b.responses) > 0 {
		content = b.responses[0]
	}
	ch <- types.StreamMessage{Type: "chunk", Content: content, Usage: &u}
	b.mu.Lock()
	b.runningN--
	b.mu.Unlock()
	close(ch)
	return ch, nil
}

func (b *blockingMockProvider) GetModelName() string          { return "blocking-mock" }
func (b *blockingMockProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "blocking-mock"}
}

func (b *blockingMockProvider) waitStarted() {
	for i := 0; i < 100; i++ {
		b.mu.Lock()
		s := b.started
		b.mu.Unlock()
		if s > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *blockingMockProvider) waitCount(n int) {
	for i := 0; i < 200; i++ {
		b.mu.Lock()
		s := b.started
		b.mu.Unlock()
		if s >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *blockingMockProvider) running() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runningN
}

func (b *blockingMockProvider) releaseAll() {
	b.mu.Lock()
	b.released = true
	w := b.wake
	if w != nil {
		close(w)
	}
	b.mu.Unlock()
}
