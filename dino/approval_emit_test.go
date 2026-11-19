package dino

import (
	"context"
	"testing"
	"time"
)

// TestApprovalStoreEmitsRequest 审批请求必须在**等待之前**推给消费方。
// 没有这条事件，上层无从得知"有个审批在等"，用户只能等超时。
func TestApprovalStoreEmitsRequest(t *testing.T) {
	store := NewApprovalStore(time.Second)
	var gotID, gotTool string
	store.SetSessionEmitter("s1", func(requestID, toolName, arguments string) {
		gotID, gotTool = requestID, toolName
	})
	store.SetSender(&autoApproveSender{store: store})

	approved, err := store.RequestApproval(context.Background(), "s1", "bash", `{"command":"ls"}`)
	if err != nil || !approved {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
	if gotID == "" || gotTool != "bash" {
		t.Fatalf("emitter not called: id=%q tool=%q", gotID, gotTool)
	}
}

// TestApprovalStoreManualBlocks 手动模式下没有回应时阻塞到超时。
func TestApprovalStoreManualBlocks(t *testing.T) {
	store := NewApprovalStore(80 * time.Millisecond)
	store.SetSessionEmitter("s2", func(string, string, string) {})
	// 不设 sender 的自动答复：只有 emitter，没有人工回应
	store.SetSender(&noopSender{})

	start := time.Now()
	approved, err := store.RequestApproval(context.Background(), "s2", "bash", "{}")
	elapsed := time.Since(start)
	if approved {
		t.Fatal("should not be approved without a responder")
	}
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("returned too early (%v)，应当等到超时", elapsed)
	}
}

type autoApproveSender struct {
	store *ApprovalStore
}

func (s *autoApproveSender) SendToolApprovalRequest(sessionID, requestID, toolName, arguments string) {
	go s.store.Respond(requestID, true)
}
func (s *autoApproveSender) SendToolApprovalResponse(string, string, string, bool) {}

type noopSender struct{}

func (s *noopSender) SendToolApprovalRequest(string, string, string, string) {}
func (s *noopSender) SendToolApprovalResponse(string, string, string, bool)  {}

// TestApprovalStoreNoTimeoutWaitsForever timeout=0 表示永不超时：
// 没有答复就一直等，直到 ctx 取消。手动审批模式依赖这个语义。
func TestApprovalStoreNoTimeoutWaitsForever(t *testing.T) {
	store := NewApprovalStore(0)
	store.SetSessionEmitter("s3", func(string, string, string) {})
	store.SetSender(&noopSender{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		approved, err := store.RequestApproval(ctx, "s3", "bash", "{}")
		if approved {
			done <- nil
			return
		}
		done <- err
	}()

	// 远超任何合理的"默认超时"，仍然不该返回
	select {
	case <-done:
		t.Fatal("RequestApproval returned without an answer (should wait forever)")
	case <-time.After(300 * time.Millisecond):
		// 正确：还在等
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ctx error after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after ctx cancel")
	}
}
