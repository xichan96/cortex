package dino

import (
	"context"
	"runtime"
	"testing"
	"time"

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
