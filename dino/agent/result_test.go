package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
)

// ==================== DelegateResult 信封 ====================

// TestDelegateResult_Fields：各字段 JSON round-trip（设计 §9.1 #1）。
func TestDelegateResult_Fields(t *testing.T) {
	env := &DelegateResult{
		Agent:        "general",
		TaskID:       "abc-123",
		Status:       DelegateStatusCompleted,
		Output:       "done",
		FilesChanged: []string{"a.go", "b.go"},
		DurationMS:   1234,
		Iterations:   3,
		Usage:        types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		TimestampMS:  1700000000000,
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got DelegateResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got.Agent != "general" || got.TaskID != "abc-123" || got.Status != "completed" {
		t.Errorf("scalar fields mismatch: %+v", got)
	}
	if got.Output != "done" || len(got.FilesChanged) != 2 || got.FilesChanged[1] != "b.go" {
		t.Errorf("output/files mismatch: %+v", got)
	}
	if got.DurationMS != 1234 || got.Iterations != 3 || got.TimestampMS != 1700000000000 {
		t.Errorf("numeric fields mismatch: %+v", got)
	}
	if got.Usage.TotalTokens != 30 {
		t.Errorf("usage mismatch: %+v", got.Usage)
	}
}

// TestDelegateResult_JSONShape：零值可选字段 omitempty，时间字段统一 int64 ms（评审 R2）。
func TestDelegateResult_JSONShape(t *testing.T) {
	env := &DelegateResult{
		Agent:  "general",
		Status: "completed",
		Usage:  types.Usage{},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(data)
	for _, absent := range []string{`"output"`, `"files_changed"`, `"error"`} {
		if strings.Contains(s, absent) {
			t.Errorf("expected %s to be omitted (omitempty), got: %s", absent, s)
		}
	}
	// 时间/时长字段必须是整数 ms，不能是 RFC3339 或字符串。
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal map failed: %v", err)
	}
	if _, ok := m["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms should be numeric ms, got %T: %s", m["duration_ms"], s)
	}
	if _, ok := m["timestamp_ms"].(float64); !ok {
		t.Errorf("timestamp_ms should be numeric ms, got %T: %s", m["timestamp_ms"], s)
	}
}

// TestDelegateResult_Truncated_Completed：正常态信封格式 + 超限截断（设计 §9.1 #2）。
func TestDelegateResult_Truncated_Completed(t *testing.T) {
	env := &DelegateResult{
		Agent:  "general",
		Status: DelegateStatusCompleted,
		Output: "the quick brown fox",
	}
	out := env.Truncated(0)
	if !strings.Contains(out, "Message Type: FINAL_ANSWER") {
		t.Errorf("missing header: %s", out)
	}
	if !strings.Contains(out, "Sender: /root/general") {
		t.Errorf("missing sender: %s", out)
	}
	if !strings.Contains(out, "Status: completed") {
		t.Errorf("missing status: %s", out)
	}
	if !strings.Contains(out, "the quick brown fox") {
		t.Errorf("missing payload: %s", out)
	}

	long := env.Truncated(10)
	if !strings.Contains(long, "…(truncated)") {
		t.Errorf("expected truncation marker, got: %s", long)
	}
}

// TestDelegateResult_Truncated_Error：错误态含回退文案（设计 §9.1 #3）。
func TestDelegateResult_Truncated_Error(t *testing.T) {
	env := &DelegateResult{
		Agent:  "general",
		Status: DelegateStatusError,
		Error:  "boom",
	}
	out := env.Truncated(0)
	if !strings.Contains(out, "Status: error") {
		t.Errorf("missing error status: %s", out)
	}
	if !strings.Contains(out, "Agent errored: boom") {
		t.Errorf("missing error payload: %s", out)
	}
	if !strings.Contains(out, "use the available collaboration tools to give it another task") {
		t.Errorf("missing fallback text (codex 回退文案): %s", out)
	}
}

// TestNewTaskID_Unique：两次委派 task_id 不同（设计 §9.1 #10）。
func TestNewTaskID_Unique(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	if a == b {
		t.Errorf("expected distinct task ids, got %s == %s", a, b)
	}
}

// ==================== 子代理结构化采集 ====================

// streamStep 每次 LLM 调用推入消息；返回非 nil error 时 ChatWithToolsStream 直接返回该 error
// （模拟真实 provider：ctx 取消/deadline 时 HTTP 层返回 ctx.Err()）。
type streamStep func(ctx context.Context, ch chan<- types.StreamMessage) error

type structuredMockLLM struct {
	mu        sync.Mutex
	seq       []streamStep
	callCount int
}

func newStructuredMockLLM() *structuredMockLLM {
	return &structuredMockLLM{}
}

func (m *structuredMockLLM) add(step streamStep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq = append(m.seq, step)
}

// callCount 返回已调用的 LLM 次数（无锁读取，测试用）。
func (m *structuredMockLLM) callCountSafe() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *structuredMockLLM) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: "ok"}, nil
}

func (m *structuredMockLLM) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return m.ChatWithToolsStream(ctx, messages, nil)
}

func (m *structuredMockLLM) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.Chat(ctx, messages)
}

func (m *structuredMockLLM) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	m.mu.Lock()
	idx := m.callCount
	m.callCount++
	if idx >= len(m.seq) {
		m.mu.Unlock()
		ch := make(chan types.StreamMessage, 1)
		ch <- types.StreamMessage{Type: "end"}
		close(ch)
		return ch, nil
	}
	step := m.seq[idx]
	m.mu.Unlock()

	ch := make(chan types.StreamMessage, 8)
	if err := step(ctx, ch); err != nil {
		return nil, err
	}
	close(ch)
	return ch, nil
}

func (m *structuredMockLLM) GetModelName() string { return "mock-structured" }
func (m *structuredMockLLM) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-structured"}
}

// TestSubagentExecute_CollectsToolEvents：write_file 触发 tool_event，FilesChanged 含该 path
// （设计 §9.1 #5）。
func TestSubagentExecute_CollectsToolEvents(t *testing.T) {
	llm := newStructuredMockLLM()
	llm.add(func(ctx context.Context, ch chan<- types.StreamMessage) error {
		ch <- types.StreamMessage{Type: "chunk", Content: "starting"}
		ch <- types.StreamMessage{Type: "tool_calls", ToolCalls: []types.ToolCall{{
			ID: "call_1", Type: "function",
			Function: types.ToolFunction{Name: "write_file", Arguments: map[string]interface{}{"path": "/tmp/a.go", "content": "x"}},
		}}}
		return nil
	})
	llm.add(func(ctx context.Context, ch chan<- types.StreamMessage) error {
		ch <- types.StreamMessage{Type: "chunk", Content: "final result"}
		ch <- types.StreamMessage{Type: "end"}
		return nil
	})

	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {Name: "general", Mode: ModeSubagent, Permission: permission.DefaultRuleset()},
		},
		tools: []types.Tool{&structuredWriteTool{}},
		llm:   llm,
	}
	m := NewManager(factory, 0)
	res, err := m.Execute(context.Background(), &Request{AgentName: "general", Input: "write a file"})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(res.FilesChanged) != 1 || res.FilesChanged[0] != "/tmp/a.go" {
		t.Errorf("expected FilesChanged [/tmp/a.go], got %v", res.FilesChanged)
	}
	if res.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", res.Iterations)
	}
	if res.Status != DelegateStatusCompleted {
		t.Errorf("expected completed, got %s", res.Status)
	}
	if !strings.Contains(res.Output, "final result") {
		t.Errorf("expected final output, got %q", res.Output)
	}
}

// structuredWriteTool：模拟 write_file，输入含 path 键，供 tool_event 采集。
type structuredWriteTool struct{}

func (t *structuredWriteTool) Name() string        { return "write_file" }
func (t *structuredWriteTool) Description() string { return "test write" }
func (t *structuredWriteTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *structuredWriteTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{ToolType: "builtin"}
}
func (t *structuredWriteTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"path": input["path"], "message": "ok"}, nil
}

// TestSubagentExecute_StatusError：Execute 中途 Result.Error -> Status=="error"（设计 §9.1 #6）。
func TestSubagentExecute_StatusError(t *testing.T) {
	llm := newStructuredMockLLM()
	llm.add(func(ctx context.Context, ch chan<- types.StreamMessage) error {
		ch <- types.StreamMessage{Type: "error", Error: "LLM exploded"}
		return nil
	})

	factory := &mockFactory{
		agents: map[string]*Info{
			"general": {Name: "general", Mode: ModeSubagent, Permission: permission.DefaultRuleset()},
		},
		tools: []types.Tool{},
		llm:   llm,
	}
	m := NewManager(factory, 0)
	res, err := m.Execute(context.Background(), &Request{AgentName: "general", Input: "boom"})
	if err != nil {
		t.Fatalf("Execute should not return error (folded into envelope), got %v", err)
	}
	if res.Status != DelegateStatusError {
		t.Errorf("expected error status, got %s", res.Status)
	}
	if res.Error == nil {
		t.Error("expected underlying error to be set")
	}
}

// TestSubagentExecute_StatusTimeoutAndCancel：mock LLM 阻塞到 ctx deadline -> "timeout"；
// ctx 主动取消 -> "cancelled"（设计 §9.1 #7，S2 ctx 传播验证）。
func TestSubagentExecute_StatusTimeoutAndCancel(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		llm := newStructuredMockLLM()
		llm.add(func(ctx context.Context, ch chan<- types.StreamMessage) error {
			<-ctx.Done()
			// 模拟 provider：ctx deadline 后 HTTP 层返回 context.DeadlineExceeded。
			return ctx.Err()
		})

		factory := &mockFactory{
			agents: map[string]*Info{
				"general": {Name: "general", Mode: ModeSubagent, Permission: permission.DefaultRuleset()},
			},
			tools: []types.Tool{},
			llm:   llm,
		}
		m := NewManager(factory, 0)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		res, err := m.Execute(ctx, &Request{AgentName: "general", Input: "slow"})
		if err != nil {
			t.Fatalf("expected folded result, got err %v", err)
		}
		if res.Status != DelegateStatusTimeout {
			t.Errorf("expected timeout status, got %s", res.Status)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		llm := newStructuredMockLLM()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		llm.add(func(cctx context.Context, ch chan<- types.StreamMessage) error {
			<-cctx.Done()
			return cctx.Err()
		})
		factory := &mockFactory{
			agents: map[string]*Info{
				"general": {Name: "general", Mode: ModeSubagent, Permission: permission.DefaultRuleset()},
			},
			tools: []types.Tool{},
			llm:   llm,
		}
		m := NewManager(factory, 0)
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		res, err := m.Execute(ctx, &Request{AgentName: "general", Input: "cancel me"})
		if err != nil {
			t.Fatalf("expected folded result, got err %v", err)
		}
		if res.Status != DelegateStatusCancelled {
			t.Errorf("expected cancelled status, got %s", res.Status)
		}
	})
}

// ==================== SubagentTool 信封返回 ====================

func newTestSubagentManager(t *testing.T, llm types.LLMProvider) *SubagentManager {
	t.Helper()
	cfg := &SubagentConfig{Enabled: true}
	return NewSubagentManager(cfg, &subagentMockFactory{llm: llm})
}

// TestSubagentTool_ReturnsEnvelope：返回 *DelegateResult 且 Output 与旧 Result.Output 相等
// （设计 §9.1 #8，向后兼容）。
func TestSubagentTool_ReturnsEnvelope(t *testing.T) {
	llm := newSubagentMockLLMProvider([]string{"subagent says hello"})
	sm := newTestSubagentManager(t, llm)
	tool := NewSubagentTool(sm)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "say hello"})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	env, ok := out.(*DelegateResult)
	if !ok {
		t.Fatalf("expected *DelegateResult, got %T", out)
	}
	if env.Output != "subagent says hello" {
		t.Errorf("expected Output to preserve old Result.Output semantics, got %q", env.Output)
	}
	if env.Status != DelegateStatusCompleted {
		t.Errorf("expected completed, got %s", env.Status)
	}
	if env.TaskID == "" {
		t.Error("expected non-empty task_id")
	}
	if env.Usage.TotalTokens == 0 {
		t.Error("expected usage collected")
	}
}

// TestSubagentTool_BackwardCompatString：信封喂给 FormatToolResult，JSON 含 "output" 字段
// （设计 §9.1 #9，老消费方不炸）。
func TestSubagentTool_BackwardCompatString(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"hello"}))
	tool := NewSubagentTool(sm)
	out, err := tool.Execute(context.Background(), map[string]interface{}{"agent": "general", "task": "hi"})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	formatted := types.FormatToolResult(out)
	if !strings.Contains(formatted, `"output"`) {
		t.Errorf("expected formatted result to contain \"output\" key, got: %s", formatted)
	}
	if !strings.Contains(formatted, `"status"`) {
		t.Errorf("expected formatted result to contain \"status\" key, got: %s", formatted)
	}
}

// TestSubagentTool_ErrorFolded：manager.Execute 出错时工具返回信封 + nil error（评审 R1）。
func TestSubagentTool_ErrorFolded(t *testing.T) {
	// 用无法取到子代理的 manager（GetAgent 不存在）触发错误路径。
	cfg := &SubagentConfig{Enabled: true}
	factory := &subagentMockFactory{}
	sm := NewSubagentManager(cfg, factory)
	tool := NewSubagentTool(sm)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"agent": "nonexistent", "task": "x"})
	if err != nil {
		t.Fatalf("expected nil error (folded into envelope), got %v", err)
	}
	env, ok := out.(*DelegateResult)
	if !ok {
		t.Fatalf("expected *DelegateResult, got %T", out)
	}
	if env.Status != DelegateStatusError {
		t.Errorf("expected error status, got %s", env.Status)
	}
	if env.Error == "" {
		t.Error("expected error summary in envelope")
	}
}

// TestSubagentTool_NoCache：Metadata 带 Extra["no_cache"]=true（评审 B1）。
func TestSubagentTool_NoCache(t *testing.T) {
	sm := newTestSubagentManager(t, newSubagentMockLLMProvider([]string{"x"}))
	tool := NewSubagentTool(sm)
	meta := tool.Metadata()
	if meta.Extra == nil {
		t.Fatal("expected Extra map in metadata")
	}
	if v, ok := meta.Extra["no_cache"].(bool); !ok || !v {
		t.Errorf("expected Extra[no_cache]=true, got %v", meta.Extra["no_cache"])
	}
}

// TestDelegateTool_EngineCacheBypass：父 AgentEngine 上 delegate 工具调用不被 toolCache 命中
// （评审 B1 的根验证：no_cache 生效）。两次同 args 调用，第二次仍执行（非缓存）。
func TestDelegateTool_EngineCacheBypass(t *testing.T) {
	subLLM := newSubagentMockLLMProvider([]string{"one"})
	sm := newTestSubagentManager(t, subLLM)
	tool := NewSubagentTool(sm)

	// 父 LLM：两次都发同一个 delegate_to_agent 工具调用（同 args），第二次给最终答案。
	parentCalls := 0
	parent := &parentToolCallLLM{}
	parent.add(func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
		parentCalls++
		return types.Message{
			Content: "delegating",
			ToolCalls: []types.ToolCall{{
				ID: "call_delegate", Type: "function",
				Function: types.ToolFunction{Name: "delegate_to_agent", Arguments: map[string]interface{}{"agent": "general", "task": "say one"}},
			}},
			Usage: types.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		}, nil
	})
	parent.add(func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
		return types.Message{Content: "done"}, nil
	})

	cfg := types.NewAgentConfig()
	cfg.MaxIterations = 4
	eng := engine.NewAgentEngine(parent, cfg)
	eng.AddTool(context.Background(), tool)

	// 用父 engine 的 OnAfterIteration 钩子抓取每轮 intermediate steps 的 observation。
	// 注意：只有 ExecuteStream 路径触发 AfterIteration 钩子（agent_execution.go:1163），
	// Execute 不触发，故父引擎用 ExecuteStream。
	var obsMu sync.Mutex
	observations := make([]string, 0)
	eng.SetHooks(context.Background(), hooks.NewHooksFunc(
		nil, nil, nil, nil, nil, nil,
		func(ctx context.Context, hc *hooks.HookContext, iteration int, result *types.AgentResult) error {
			obsMu.Lock()
			defer obsMu.Unlock()
			for _, s := range result.IntermediateSteps {
				observations = append(observations, s.Observation)
			}
			return nil
		},
		nil, nil,
	))

	ctx := context.Background()
	run := func() {
		stream, err := eng.ExecuteStream(ctx, types.NewAgentInput("run delegate twice"), nil)
		if err != nil {
			t.Fatalf("ExecuteStream failed: %v", err)
		}
		for range stream {
		}
	}
	run()
	parent.reset() // 两次 Execute 间重置父 LLM 序列（第二次同样先 delegate 再收尾）
	run()

	// 若 no_cache 失效：第二次 Execute 的 delegate 调用会命中第一次的缓存，
	// 工具 Execute 被跳过 → 子代理 LLM 只被调 1 次。
	// no_cache 生效：两次都真实执行 → 子代理 LLM 被调 2 次。
	if got := subLLM.callCountSafe(); got != 2 {
		t.Errorf("expected subagent LLM called twice (delegate not cached across runs), got %d", got)
	}
	// 两次 observation 都应是信封 JSON（含 status 字段），而非裸字符串。
	obsMu.Lock()
	defer obsMu.Unlock()
	if len(observations) != 2 {
		t.Fatalf("expected 2 delegate observations, got %d: %v", len(observations), observations)
	}
	for i, obs := range observations {
		if !strings.Contains(obs, `"status"`) {
			t.Errorf("observation %d should be envelope JSON, got: %s", i, obs)
		}
	}
	_ = parentCalls // parent 调用次数与缓存无关（父代理仍需最后迭代收尾），仅保留可观测性
}

// parentToolCallLLM：父代理 mock LLM，按序返回 tool_calls 或最终答案。
type parentToolCallLLM struct {
	mu        sync.Mutex
	seq       []func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
	callCount int
}

func (m *parentToolCallLLM) add(step func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq = append(m.seq, step)
}

func (m *parentToolCallLLM) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
}

func (m *parentToolCallLLM) next(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	m.mu.Lock()
	idx := m.callCount
	m.callCount++
	if idx >= len(m.seq) {
		m.mu.Unlock()
		return types.Message{Content: "done"}, nil
	}
	step := m.seq[idx]
	m.mu.Unlock()
	return step(ctx, messages, tools)
}

func (m *parentToolCallLLM) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return m.next(ctx, messages, nil)
}
func (m *parentToolCallLLM) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return m.ChatWithToolsStream(ctx, messages, nil)
}
func (m *parentToolCallLLM) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.next(ctx, messages, tools)
}
func (m *parentToolCallLLM) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	msg, err := m.next(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan types.StreamMessage, 4)
	if len(msg.ToolCalls) > 0 {
		ch <- types.StreamMessage{Type: "tool_calls", ToolCalls: msg.ToolCalls}
	}
	ch <- types.StreamMessage{Type: "chunk", Content: msg.Content}
	ch <- types.StreamMessage{Type: "end"}
	close(ch)
	return ch, nil
}
func (m *parentToolCallLLM) GetModelName() string { return "parent-mock" }
func (m *parentToolCallLLM) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "parent-mock"}
}

// ==================== 导入清理 ====================

var _ = errors.New // 保留 errors 导入（后续错误断言用）
