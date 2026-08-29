package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	agentutils "github.com/xichan96/cortex/agent/utils"
)

// streamProvider 测试用 LLM provider：单次流式返回一个 chunk + 结束。
type streamProvider struct {
	mu      sync.Mutex
	streams int
	lastIn  string
}

func (p *streamProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: "done", Usage: types.Usage{TotalTokens: 10}}, nil
}

func (p *streamProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return p.ChatWithToolsStream(ctx, messages, nil)
}

func (p *streamProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return p.Chat(ctx, messages)
}

func (p *streamProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	p.mu.Lock()
	p.streams++
	if len(messages) > 0 {
		p.lastIn = messages[len(messages)-1].Content
	}
	p.mu.Unlock()

	u := types.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}
	ch := make(chan types.StreamMessage, 2)
	ch <- types.StreamMessage{Type: "chunk", Content: "processed: " + p.lastInput(messages), Usage: &u}
	ch <- types.StreamMessage{Type: "end", Usage: &u}
	close(ch)
	return ch, nil
}

func (p *streamProvider) lastInput(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return ""
}

func (p *streamProvider) streamCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams
}

func (p *streamProvider) GetModelName() string          { return "stream-mock" }
func (p *streamProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "stream-mock"}
}

// fakeWakeSource 测试用唤醒源：可手动注入 payload，验证 Session.run 的 wakeCh 分支。
type fakeWakeSource struct {
	ch       chan struct{}
	mu       sync.Mutex
	payloads []WakePayload
}

func newFakeWakeSource() *fakeWakeSource {
	return &fakeWakeSource{ch: make(chan struct{}, 4)}
}

func (f *fakeWakeSource) Wake() <-chan struct{} { return f.ch }

func (f *fakeWakeSource) Collect() []WakePayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.payloads
	f.payloads = nil
	return out
}

func (f *fakeWakeSource) fire(payloads ...WakePayload) {
	f.mu.Lock()
	f.payloads = append(f.payloads, payloads...)
	f.mu.Unlock()
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

// fakeFactory 满足 SessionFactory 最小接口。
type fakeFactory struct{}

func (fakeFactory) RecordLoop(sessionID string, action agentutils.LoopDetectAction) {}
func (fakeFactory) RecordTokens(ctx context.Context, sessionID string, tokens int) {}
func (fakeFactory) Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult {
	return nil
}

func newTestEngine(p *streamProvider) *engine.AgentEngine {
	cfg := types.NewAgentConfig()
	cfg.MaxIterations = 2
	cfg.MaxCompletionTokens = 128
	eng := engine.NewAgentEngine(p, cfg)
	return eng
}

// TestSession_NoWakeByDefault：无唤醒源（NoWakeSource）时行为同现状——Session.run
// 不服务唤醒分支，正常处理用户 turn。
func TestSession_NoWakeByDefault(t *testing.T) {
	prov := &streamProvider{}
	s := NewSession("no-wake", newTestEngine(prov), fakeFactory{}, context.Background(), DefaultConfig(), nil, nil, NoWakeSource())
	s.Start()
	defer s.Close()

	// 发一个用户输入，turn 应正常完成（EventTypeDone）。
	done := make(chan struct{})
	id := s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.IsDone() {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))
	defer s.Unsubscribe(id)

	s.Input() <- "hello"
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected turn to complete without wake source")
	}
}

// TestSession_WakeFiresTurn：唤醒信号 → onSubagentCompletion → Collect 的 payload
// 注入新 turn（验证 wakeCh 分支被服务且执行真实 turn）。
func TestSession_WakeFiresTurn(t *testing.T) {
	prov := &streamProvider{}
	src := newFakeWakeSource()
	s := NewSession("wake", newTestEngine(prov), fakeFactory{}, context.Background(), DefaultConfig(), nil, nil, src)
	s.Start()
	defer s.Close()

	done := make(chan struct{})
	id := s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.IsDone() {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))
	defer s.Unsubscribe(id)

	// 先跑一个用户 turn 把 run 循环拉起来（否则 wakeCh 可能未就绪）。
	s.Input() <- "first"
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("first turn did not complete")
	}

	// 注入唤醒：应触发第二个 turn，provider 收到含 payload.Text 的输入。
	src.fire(WakePayload{TaskID: "t1", Text: "subagent completed result"})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("wake turn did not fire")
	}

	prov.mu.Lock()
	last := prov.lastIn
	streams := prov.streams
	prov.mu.Unlock()
	if streams < 2 {
		t.Errorf("expected >=2 streams (user + wake), got %d", streams)
	}
	if last != "subagent completed result" {
		t.Errorf("expected wake turn input to be payload text, got %q", last)
	}
}

// TestWakeAdapter_CollectDrainsOnce：Collect 取走全部后重复调用为空（§10.2 #20 语义）。
func TestWakeAdapter_CollectDrainsOnce(t *testing.T) {
	src := newFakeWakeSource()
	src.fire(WakePayload{TaskID: "a", Text: "A"}, WakePayload{TaskID: "b", Text: "B"})
	got := src.Collect()
	if len(got) != 2 {
		t.Fatalf("expected 2 payloads, got %d", len(got))
	}
	if again := src.Collect(); len(again) != 0 {
		t.Errorf("expected empty second Collect, got %d", len(again))
	}
}

// TestSession_UserInputPriority：唤醒与用户输入并发时用户输入不被吞。
// 唤醒 turn 可重入（消息留 mailbox 直到 Collect），用户输入始终由 input 分支处理。
func TestSession_UserInputPriority(t *testing.T) {
	prov := &streamProvider{}
	src := newFakeWakeSource()
	s := NewSession("prio", newTestEngine(prov), fakeFactory{}, context.Background(), DefaultConfig(), nil, nil, src)
	s.Start()
	defer s.Close()

	done := make(chan struct{})
	id := s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.IsDone() {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))
	defer s.Unsubscribe(id)

	// 同时塞唤醒信号 + 用户输入。
	src.fire(WakePayload{TaskID: "w", Text: "wake text"})
	s.Input() <- "user text"

	// 两个 turn 都应执行（先唤醒后用户，或先用户后唤醒），用户消息不能丢。
	turns := 0
	for turns < 2 {
		select {
		case <-done:
			turns++
		case <-time.After(3 * time.Second):
			t.Fatalf("expected both turns, only got %d", turns)
		}
	}
}
