package agent

import (
	"sync"
	"testing"
	"time"
)

func testResult(status string) *DelegateResult {
	return &DelegateResult{
		Agent:       "general",
		TaskID:      "task-" + status,
		Status:      status,
		Output:      "output " + status,
		TimestampMS: time.Now().UnixMilli(),
	}
}

// TestMailbox_PutDrainOnce：Put→Drain 一次可取，二次为 nil（只读一次语义）。
func TestMailbox_PutDrainOnce(t *testing.T) {
	mb := NewMailbox(0, 0)
	r := testResult(DelegateStatusCompleted)
	if err := mb.Put("t1", r); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got := mb.Drain("t1"); got != r {
		t.Errorf("expected first Drain to return the result, got %v", got)
	}
	if got := mb.Drain("t1"); got != nil {
		t.Errorf("expected second Drain to return nil, got %v", got)
	}
	if mb.Len() != 0 {
		t.Errorf("expected empty mailbox after drain, Len=%d", mb.Len())
	}
}

// TestMailbox_PutDrainAll：多个 Put→DrainAll 按到达序返回且清空。
func TestMailbox_PutDrainAll(t *testing.T) {
	mb := NewMailbox(0, 0)
	rs := []*DelegateResult{
		testResult("a"),
		testResult("b"),
		testResult("c"),
	}
	for i, r := range rs {
		if err := mb.Put("k"+string(rune('a'+i)), r); err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}
	got := mb.DrainAll()
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	for i, r := range got {
		if r != rs[i] {
			t.Errorf("DrainAll order mismatch at %d: got %v want %v", i, r.Output, rs[i].Output)
		}
	}
	if mb.Len() != 0 {
		t.Errorf("expected empty after DrainAll, Len=%d", mb.Len())
	}
}

// TestMailbox_CapFull：cap=2 时第 3 个 Put 返回 error，旧消息不丢。
func TestMailbox_CapFull(t *testing.T) {
	mb := NewMailbox(2, 0)
	if err := mb.Put("t1", testResult("1")); err != nil {
		t.Fatalf("Put 1 failed: %v", err)
	}
	if err := mb.Put("t2", testResult("2")); err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}
	if err := mb.Put("t3", testResult("3")); err == nil {
		t.Fatal("expected error on 3rd Put when cap=2")
	} else if !IsMailboxFullError(err) {
		t.Errorf("expected mailbox-full error, got %v", err)
	}
	// 旧消息不丢。
	if got := mb.Peek("t1"); got == nil {
		t.Error("expected t1 still present after cap-full put")
	}
	if got := mb.Peek("t2"); got == nil {
		t.Error("expected t2 still present after cap-full put")
	}
	// 覆盖已存在 key 不算新增，不触发 cap。
	if err := mb.Put("t1", testResult("1b")); err != nil {
		t.Errorf("overwrite existing key should not hit cap: %v", err)
	}
}

// TestMailbox_TTLExpiry：TTL 到期的条目在 Peek 时被清理。
func TestMailbox_TTLExpiry(t *testing.T) {
	mb := NewMailbox(0, 30*time.Millisecond)
	if err := mb.Put("t1", testResult("1")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got := mb.Peek("t1"); got == nil {
		t.Fatal("expected t1 present before TTL expiry")
	}
	time.Sleep(50 * time.Millisecond)
	if got := mb.Peek("t1"); got != nil {
		t.Error("expected t1 cleaned up after TTL expiry")
	}
	if mb.Len() != 0 {
		t.Errorf("expected empty after TTL sweep, Len=%d", mb.Len())
	}
}

// TestMailbox_SubscribeOnceRace：Put 与 SubscribeOnce 竞态（先订阅/后订阅/中间 Put）
// 都能收到。用并发 goroutine 跑 -race 验证锁正确性。
func TestMailbox_SubscribeOnceRace(t *testing.T) {
	for round := 0; round < 50; round++ {
		mb := NewMailbox(0, 0)
		done := make(chan struct{})
		var wg sync.WaitGroup
		// 订阅 goroutine：注册 SubscribeOnce。
		wg.Add(1)
		go func() {
			defer wg.Done()
			mb.SubscribeOnce("t1", func() { close(done) })
		}()
		// Put goroutine：并发 Put。
		wg.Add(1)
		go func() {
			defer wg.Done()
			mb.Put("t1", testResult("1"))
		}()
		wg.Wait()
		// 无论先订阅还是先 Put，订阅者都应收到通知（Put 在订阅后触发，或消息已存在立即触发）。
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatalf("round %d: subscriber did not receive notification", round)
		}
	}
}

// TestMailbox_SubscribeOnce_AlreadyPresent：消息已存在时 SubscribeOnce 立即触发且不注册。
func TestMailbox_SubscribeOnce_AlreadyPresent(t *testing.T) {
	mb := NewMailbox(0, 0)
	if err := mb.Put("t1", testResult("1")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	fired := false
	id := mb.SubscribeOnce("t1", func() { fired = true })
	if id != "" {
		t.Errorf("expected empty sub id when message already present, got %q", id)
	}
	if !fired {
		t.Error("expected immediate trigger when message present")
	}
}

// TestMailbox_SubscribeOnce_Unsubscribe：反注册后不再触发。
func TestMailbox_SubscribeOnce_Unsubscribe(t *testing.T) {
	mb := NewMailbox(0, 0)
	fired := false
	id := mb.SubscribeOnce("t1", func() { fired = true })
	if id == "" {
		t.Fatal("expected non-empty sub id")
	}
	mb.Unsubscribe("t1", id)
	mb.Put("t1", testResult("1"))
	if fired {
		t.Error("expected no trigger after unsubscribe")
	}
}

// TestMailbox_SubscribeAll_NotifyOnly：SubscribeAll 回调收到通知但消息仍在
// （评审 BLOCKER 2：适配器只通知不 Drain，session 端 DrainAll）。
func TestMailbox_SubscribeAll_NotifyOnly(t *testing.T) {
	mb := NewMailbox(0, 0)
	notified := make(chan struct{}, 1)
	mb.SubscribeAll(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	if err := mb.Put("t1", testResult("1")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	select {
	case <-notified:
	case <-time.After(1 * time.Second):
		t.Fatal("expected SubscribeAll notification")
	}
	// 消息仍在（适配器未 Drain），session 端 DrainAll 可取。
	if got := mb.Peek("t1"); got == nil {
		t.Fatal("expected message still present after SubscribeAll notify (adapter must not Drain)")
	}
	if got := mb.DrainAll(); len(got) != 1 {
		t.Errorf("expected 1 result via DrainAll, got %d", len(got))
	}
}

// TestMailbox_Drop：Drop 后清空消息与订阅。
func TestMailbox_Drop(t *testing.T) {
	mb := NewMailbox(0, 0)
	mb.Put("t1", testResult("1"))
	fired := false
	mb.SubscribeOnce("t2", func() { fired = true })
	mb.Drop()
	if mb.Len() != 0 {
		t.Errorf("expected empty after Drop, Len=%d", mb.Len())
	}
	mb.Put("t2", testResult("2"))
	if fired {
		t.Error("expected no trigger after Drop cleared subscriptions")
	}
}
