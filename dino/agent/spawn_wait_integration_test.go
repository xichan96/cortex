package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// TestSpawnWait_EndToEnd：真实 SubagentManager + mock LLM，spawn 两个并行 → 两个 wait，
// 顺序无关性（subagent-s3s4 §10.2 #1）。用 echo provider（按任务文本回显）验证 task_id
// 精确寻址：并发下 mock 响应轮转不可靠，但回显内容是任务本身，天然确定。
func TestSpawnWait_EndToEnd(t *testing.T) {
	sm := newTestSubagentManager(t, &echoMockProvider{})
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	sm.SetNotifier(NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true))

	spawnTool := NewSpawnAgentTool(sm, func() *Mailbox { return mb }, func() AgentPath { return RootAgentPath() }, func() string { return "sess-io" })
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 10*time.Second)

	// 并行 spawn 两个。
	taskA := spawn(t, spawnTool, "task A")
	taskB := spawn(t, spawnTool, "task B")
	if taskA == taskB {
		t.Fatal("expected distinct task_ids")
	}

	// 顺序无关：无论先 wait 谁，都能拿到正确结果（按 task_id 精确寻址）。
	ra := waitFor(t, waitTool, taskA, "task A")
	rb := waitFor(t, waitTool, taskB, "task B")
	if strings.Contains(ra, "task A") != true {
		t.Errorf("task A should carry its own task text, got %q", ra)
	}
	if strings.Contains(rb, "task B") != true {
		t.Errorf("task B should carry its own task text, got %q", rb)
	}
}

// echoMockProvider 回显最后一次用户输入（按任务文本确定输出，供 task_id 寻址测试）。
type echoMockProvider struct{}

func (e *echoMockProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: lastUserText(messages)}, nil
}
func (e *echoMockProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return e.ChatWithToolsStream(ctx, messages, nil)
}
func (e *echoMockProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return e.Chat(ctx, messages)
}
func (e *echoMockProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	text := lastUserText(messages)
	u := types.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}
	ch := make(chan types.StreamMessage, 1)
	ch <- types.StreamMessage{Type: "chunk", Content: text, Usage: &u}
	close(ch)
	return ch, nil
}
func (e *echoMockProvider) GetModelName() string          { return "echo-mock" }
func (e *echoMockProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "echo-mock"}
}

func lastUserText(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return "echo"
}

func spawn(t *testing.T, tool *SpawnAgentTool, task string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": task})
	if err != nil {
		t.Fatalf("spawn %q failed: %v", task, err)
	}
	tid, _ := out.(map[string]interface{})["task_id"].(string)
	if tid == "" {
		t.Fatalf("spawn %q: empty task_id", task)
	}
	return tid
}

func waitFor(t *testing.T, tool *WaitAgentTool, taskID, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := tool.Execute(context.Background(), map[string]interface{}{"task_id": taskID})
		if err != nil {
			t.Fatalf("wait %q error: %v", taskID, err)
		}
		wm := out.(map[string]interface{})
		if wm["completed"] == true {
			msg, _ := wm["message"].(string)
			return msg
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait for %q timed out before completion", taskID)
	return ""
}

// TestWaitAgent_AndAdapter_Concurrent：wait_agent 独占 Drain 与 SubscribeAll 通知并发
// （评审 BLOCKER-2 回归）：spawn 两个，一个走 wait_agent，一个走唤醒路径（SubscribeAll
// 通知后 DrainAll），确保无假超时、无丢结果。用 task_id 判别（mock 响应并发分配有竞态，
// 内容不可靠，但 task_id 是 spawn 时确定的）。
func TestWaitAgent_AndAdapter_Concurrent(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"same"}))
	sm.SetMaxConcurrentSpawns(4)
	mb := NewMailbox(0, 0)
	sm.SetNotifier(NewCompletionNotifier(func(string) *Mailbox { return mb }, nil, true))
	spawnTool := NewSpawnAgentTool(sm, func() *Mailbox { return mb }, func() AgentPath { return RootAgentPath() }, func() string { return "sess-c" })
	waitTool := NewWaitAgentTool(sm, func() *Mailbox { return mb }, 10*time.Second)

	taskW := spawn(t, spawnTool, "wait-task")
	taskN := spawn(t, spawnTool, "notify-task")

	// 全局订阅（唤醒适配器行为：只通知，不 Drain）。
	notified := make(chan struct{}, 1)
	mb.SubscribeAll(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})

	var wg sync.WaitGroup
	var waitMsg string
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitMsg = waitFor(t, waitTool, taskW, "same")
	}()

	// 等全局通知到（任一 spawn 完成）→ wait_agent 开始阻塞拉取；同时轮询 taskN
	// 的结果落 mailbox（确定 taskN 完成），再用 DrainAll 走唤醒路径消费它。
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("SubscribeAll never notified")
	}
	deadline := time.Now().Add(5 * time.Second)
	for mb.Peek(taskN) == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	if waitMsg == "" {
		t.Fatal("wait_agent did not receive a result")
	}
	// 唤醒路径 DrainAll：应剩 taskN 的结果（taskW 已被 wait_agent Drain 拿走）。
	got := mb.DrainAll()
	found := false
	for _, r := range got {
		if r.TaskID == taskN {
			found = true
		}
		if r.TaskID == taskW {
			t.Error("wait_agent result leaked to DrainAll (consumption arbitration broken)")
		}
	}
	if !found {
		t.Errorf("expected notify-task result in DrainAll, got %d results", len(got))
	}
}
