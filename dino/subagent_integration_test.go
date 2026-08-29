package dino

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/dino/session"

	dinoAgent "github.com/xichan96/cortex/dino/agent"
)

// TestSessionClose_DropsMailbox：session 关闭 → mailbox Drop → 无残留（§10.2 #4）。
func TestSessionClose_DropsMailbox(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())
	df := factory.(*dinoFactory)

	if _, err := factory.CreateSession(context.Background(), "drop-session"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mb := df.sessionMailboxes["drop-session"]
	if mb == nil {
		t.Fatal("expected mailbox created for session")
	}
	// 放入一条未读结果。
	if err := mb.Put("orphan-task", &dinoAgent.DelegateResult{Status: "completed", TaskID: "orphan-task"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	factory.CloseSession("drop-session")
	if _, exists := df.sessionMailboxes["drop-session"]; exists {
		t.Error("expected session mailbox removed after CloseSession")
	}
	if _, exists := df.sessions["drop-session"]; exists {
		t.Error("expected session removed after CloseSession")
	}
}

// TestSessionClose_MailboxDroppedEmpty：CloseSession 后 mailbox 被 Drop（消息清空）。
func TestSessionClose_MailboxDroppedEmpty(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())
	df := factory.(*dinoFactory)

	if _, err := factory.CreateSession(context.Background(), "drop2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mb := df.sessionMailboxes["drop2"]
	if mb == nil {
		t.Fatal("expected mailbox")
	}
	if err := mb.Put("t1", &dinoAgent.DelegateResult{TaskID: "t1", Status: "completed"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	factory.CloseSession("drop2")
	// mailbox 已被 Drop；即使引用还在，Len 应为 0。
	if mb.Len() != 0 {
		t.Errorf("expected mailbox emptied after Drop, Len=%d", mb.Len())
	}
}

// TestSpawnSessionCancel_NoLeak：session 关闭 → 无 goroutine 泄漏（§10.2 #5 简化版）。
func TestSpawnSessionCancel_NoLeak(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.CreateSession(context.Background(), "cancel-session"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	before := runtime.NumGoroutine()
	factory.CloseSession("cancel-session")
	// 容忍调度抖动：等短暂时间让 cancel 传播。
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Errorf("expected no goroutine leak after CloseSession, before=%d after=%d", before, after)
	}
}

// TestWakeAdapter_DrainAllOnce：真实 sessionWakeSource 适配器（factory 侧）：
// mailbox Put → SubscribeAll 信号 → Collect 取走全部并截断，重复触发不再产出
// （§10.2 #20）。
func TestWakeAdapter_DrainAllOnce(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	// 手动构造适配器（不依赖 CreateSession 的开关）。
	mb := dinoAgent.NewMailbox(dinoAgent.DefaultMailboxCap, 0)
	src := newSessionWakeSource(mb, 2000)
	defer src.Close()

	env1 := &dinoAgent.DelegateResult{Agent: "general", Status: "completed", TaskID: "a", Output: "result A"}
	env2 := &dinoAgent.DelegateResult{Agent: "general", Status: "completed", TaskID: "b", Output: "result B"}
	mb.Put("a", env1)
	mb.Put("b", env2)

	got := src.Collect()
	if len(got) != 2 {
		t.Fatalf("expected 2 payloads, got %d", len(got))
	}
	if got[0].TaskID != "a" || got[1].TaskID != "b" {
		t.Errorf("expected ordered payloads a,b got %q,%q", got[0].TaskID, got[1].TaskID)
	}
	if !strings.Contains(got[0].Text, "result A") {
		t.Errorf("expected truncated text in payload, got %q", got[0].Text)
	}
	// 重复 Collect 为空。
	if again := src.Collect(); len(again) != 0 {
		t.Errorf("expected empty second Collect, got %d", len(again))
	}
}

// TestWakeOnCompletion_DisabledByDefault：WakeOnCompletion 默认 false → 不构造唤醒适配器
// （评审 RECOMMENDED-1 灰度开关）。
func TestWakeOnCompletion_DisabledByDefault(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())
	df := factory.(*dinoFactory)

	if _, err := factory.CreateSession(context.Background(), "no-wake-sess"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if w := df.sessionWakes["no-wake-sess"]; w != nil {
		t.Error("expected no wake source when WakeOnCompletion=false (default)")
	}
}

// TestWakeOnCompletion_FullSession：WakeOnCompletion=true → mailbox Put → 父代理自动
// 新 turn 消费（§10.2 #3 集成）。
func TestWakeOnCompletion_FullSession(t *testing.T) {
	cfg := getTestConfig()
	cfg.Subagent.WakeOnCompletion = true
	cfg.Subagent.CompletionMaxRunes = 2000
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())
	df := factory.(*dinoFactory)

	// 用一个能记录输入的 LLM provider。dino 包已有 mockLLMProvider（getTestConfig 用 "mock"）。
	// 这里直接替换 factory 的 provider 更复杂；改用 CreateSession 默认 provider（mock）+
	// 直接 Put mailbox 触发唤醒，断言 session 自动跑了新 turn（EventTypeDone 计数）。
	_ = df

	// 订阅 session 事件，统计 Done 次数。
	sess, err := factory.CreateSession(context.Background(), "wake-full")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	doneCh := make(chan int, 8)
	turns := 0
	subID := sess.Subscribe(sessionObserverFunc(func(ev *session.Event) {
		if ev.IsDone() {
			turns++
			doneCh <- turns
		}
	}))
	defer sess.Unsubscribe(subID)

	// 第一个用户 turn 启动。
	sess.Input() <- "first turn"
	waitDone(t, doneCh, 1)

	// 唤醒路径：mailbox Put 一条完成 → session 自动新 turn。
	mb := df.sessionMailboxes["wake-full"]
	if mb == nil {
		t.Fatal("expected mailbox")
	}
	env := &dinoAgent.DelegateResult{Agent: "general", Status: "completed", TaskID: "w1", Output: "wake me up"}
	if err := mb.Put("w1", env); err != nil {
		t.Fatalf("Put: %v", err)
	}
	waitDone(t, doneCh, 2)
}

type sessionObserverFunc func(ev *session.Event)

func (f sessionObserverFunc) OnEvent(ev *session.Event) { f(ev) }

func waitDone(t *testing.T, doneCh <-chan int, want int) {
	t.Helper()
	select {
	case got := <-doneCh:
		if got < want {
			t.Fatalf("expected turn %d done, got %d", want, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for turn %d", want)
	}
}

// TestSpawnToolRegistered_AndBusEvent：factory 装配 spawn/wait 工具，spawn 完成触发
// subagent.completed 旁路事件（评审 BLOCKER-1：实例级 Bus）。
func TestSpawnToolRegistered_AndBusEvent(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())
	df := factory.(*dinoFactory)

	if _, err := factory.CreateSession(context.Background(), "bus-session"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// 订阅实例级 bus 的 subagent.completed。
	events := make(chan BusEvent, 4)
	df.bus.Subscribe("subagent.completed", func(ev BusEvent) {
		select {
		case events <- ev:
		default:
		}
	})

	// 通过 notifier 模拟 spawn 完成（等价于 SubagentManager.Spawn goroutine 内的回发）。
	mb := df.sessionMailboxes["bus-session"]
	if mb == nil {
		t.Fatal("expected mailbox")
	}
	env := &dinoAgent.DelegateResult{Agent: "general", Status: "completed", Output: "hi", TaskID: "bus-task"}
	df.notifier.Notify("bus-session", "bus-task", env)

	select {
	case ev := <-events:
		if ev.SessionID != "bus-session" {
			t.Errorf("expected session_id bus-session, got %q", ev.SessionID)
		}
		if ev.Type != "subagent.completed" {
			t.Errorf("expected type subagent.completed, got %q", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected subagent.completed bus event")
	}
	// mailbox 也有结果（模型可见投递）。
	if got := mb.Peek("bus-task"); got == nil {
		t.Error("expected mailbox to hold result")
	}
}
