package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newSpawnEnv(t *testing.T, responses []string) (*SubagentManager, *Mailbox, *SpawnAgentTool, *WaitAgentTool) {
	t.Helper()
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider(responses))
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	n := NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true)
	sm.SetNotifier(n)
	spawnTool := NewSpawnAgentTool(sm, func() *Mailbox { return mb }, func() AgentPath { return RootAgentPath() }, func() string { return "sess-spawn" })
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 30*time.Second)
	return sm, mb, spawnTool, waitTool
}

// TestWaitAgent_ReceivesCompletion：spawn 后 wait_agent 阻塞到完成，
// 返回 {completed:true, message 含 FINAL_ANSWER}。
func TestWaitAgent_ReceivesCompletion(t *testing.T) {
	sm, mb, spawnTool, waitTool := newSpawnEnv(t, []string{"final output"})

	out, err := spawnTool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "research"})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	taskID, ok := out.(map[string]interface{})["task_id"].(string)
	if !ok || taskID == "" {
		t.Fatalf("expected task_id, got %v", out)
	}

	waitOut, err := waitTool.Execute(context.Background(), map[string]interface{}{"task_id": taskID})
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	waitMap := waitOut.(map[string]interface{})
	if waitMap["completed"] != true {
		t.Errorf("expected completed=true, got %v", waitMap["completed"])
	}
	if waitMap["timed_out"] != false {
		t.Errorf("expected timed_out=false, got %v", waitMap["timed_out"])
	}
	msg, _ := waitMap["message"].(string)
	if !strings.Contains(msg, "Message Type: FINAL_ANSWER") {
		t.Errorf("expected message to contain FINAL_ANSWER header, got %q", msg)
	}
	if !strings.Contains(msg, "final output") {
		t.Errorf("expected message to contain subagent output, got %q", msg)
	}
	// 结果只读一次：再 wait 应 timed_out（已被 Drain 拿走）。用短超时避免拖慢测试。
	shortWait := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 50*time.Millisecond)
	waitAgain, _ := shortWait.Execute(context.Background(), map[string]interface{}{"task_id": taskID})
	wm := waitAgain.(map[string]interface{})
	if wm["timed_out"] != true {
		t.Errorf("expected second wait to time out (result drained once), got %v", wm["timed_out"])
	}
}

// TestWaitAgent_Timeout：未完成时超时返回 {completed:false, timed_out:true}。
func TestWaitAgent_Timeout(t *testing.T) {
	blocker := newBlockingMockProvider([]string{"x"})
	sm := newTestSubagentManager(t, blocker)
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	sm.SetNotifier(NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true))
	spawnTool := NewSpawnAgentTool(sm, func() *Mailbox { return mb }, func() AgentPath { return RootAgentPath() }, func() string { return "s" })
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 50*time.Millisecond)

	// 先 spawn（子代理阻塞在 mock），再 wait 超时。
	out, err := spawnTool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "run"})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	taskID := out.(map[string]interface{})["task_id"].(string)

	waitOut, err := waitTool.Execute(context.Background(), map[string]interface{}{"task_id": taskID})
	if err != nil {
		t.Fatalf("wait should not error on timeout, got: %v", err)
	}
	wm := waitOut.(map[string]interface{})
	if wm["completed"] != false {
		t.Errorf("expected completed=false, got %v", wm["completed"])
	}
	if wm["timed_out"] != true {
		t.Errorf("expected timed_out=true, got %v", wm["timed_out"])
	}
	blocker.releaseAll()
}

// TestWaitAgent_CtxDone：engine 外壳 timeout 先到 → ctx cancel → wait 返回 timed_out
// 信封而非 error（评审 BLOCKER-3：wait 有效上限 < engine 工具超时时，wait 先返回；
// 这里直接 cancel ctx 验证 ctx.Done 分支）。
func TestWaitAgent_CtxDone(t *testing.T) {
	blocker := newBlockingMockProvider([]string{"x"})
	sm := newTestSubagentManager(t, blocker)
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	sm.SetNotifier(NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true))
	spawnTool := NewSpawnAgentTool(sm, func() *Mailbox { return mb }, func() AgentPath { return RootAgentPath() }, func() string { return "s" })
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 30*time.Second)

	out, err := spawnTool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "run"})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	taskID := out.(map[string]interface{})["task_id"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	waitOut, err := waitTool.Execute(ctx, map[string]interface{}{"task_id": taskID})
	if err != nil {
		t.Fatalf("wait should return envelope on ctx cancel, not error: %v", err)
	}
	wm := waitOut.(map[string]interface{})
	if wm["timed_out"] != true {
		t.Errorf("expected timed_out=true on ctx cancel, got %v", wm["timed_out"])
	}
	blocker.releaseAll()
}

// TestSpawnAgentTool_ReturnsTaskID：spawn 返回 {task_id}，与 mailbox 里 DelegateResult.TaskID 一致。
func TestSpawnAgentTool_ReturnsTaskID(t *testing.T) {
	_, mb, spawnTool, _ := newSpawnEnv(t, []string{"out"})

	out, err := spawnTool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "t"})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	taskID := out.(map[string]interface{})["task_id"].(string)
	if taskID == "" {
		t.Fatal("expected non-empty task_id")
	}
	// 等 mailbox 落结果（子代理完成）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := mb.Peek(taskID); r != nil {
			if r.TaskID != taskID {
				t.Errorf("mailbox TaskID %q != spawn task_id %q", r.TaskID, taskID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mailbox did not receive result in time")
}

// TestWaitAgent_NoCache：WaitAgentTool.Metadata().Extra["no_cache"]==true（评审风险 10）。
func TestWaitAgent_NoCache(t *testing.T) {
	_, _, _, waitTool := newSpawnEnv(t, []string{"x"})
	meta := waitTool.Metadata()
	if v, ok := meta.Extra["no_cache"].(bool); !ok || !v {
		t.Error("expected wait_agent metadata no_cache=true")
	}
	// spawn 工具同理。
	_, _, spawnTool, _ := newSpawnEnv(t, []string{"x"})
	meta2 := spawnTool.Metadata()
	if v, ok := meta2.Extra["no_cache"].(bool); !ok || !v {
		t.Error("expected spawn_agent metadata no_cache=true")
	}
}

// TestWaitAgent_NoMailbox：无 mailbox 时返回 no-mailbox 信封而非挂死。
func TestWaitAgent_NoMailbox(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"x"}))
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return nil }, 30*time.Second)
	out, err := waitTool.Execute(context.Background(), map[string]interface{}{"task_id": "t1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wm := out.(map[string]interface{})
	if wm["timed_out"] != true {
		t.Errorf("expected timed_out=true for no mailbox, got %v", wm["timed_out"])
	}
}
