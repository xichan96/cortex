package chatstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	agenttypes "github.com/xichan96/cortex/agent/types"
)

// ==================== 测试用 mock ====================

// mockSummaryLLM 实现 chatstore.LLMProvider，可编程成功/失败/空。
type mockSummaryLLM struct {
	mu       sync.Mutex
	summary  string
	err      error
	lastIn   []Message
	calls    int
}

func (m *mockSummaryLLM) GenerateSummary(ctx context.Context, messages []Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastIn = append([]Message(nil), messages...)
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

// mockChatLLM 实现 types.LLMProvider（供 LLMSummaryAdapter 测试）。
type mockChatLLM struct {
	mu       sync.Mutex
	resp     string
	err      error
	lastMsgs []agenttypes.Message
	calls    int
}

func (m *mockChatLLM) Chat(ctx context.Context, msgs []agenttypes.Message) (agenttypes.Message, error) {
	return agenttypes.Message{}, nil
}
func (m *mockChatLLM) ChatStream(ctx context.Context, msgs []agenttypes.Message) (<-chan agenttypes.StreamMessage, error) {
	return nil, nil
}
func (m *mockChatLLM) ChatWithTools(ctx context.Context, msgs []agenttypes.Message, tools []agenttypes.Tool) (agenttypes.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastMsgs = append([]agenttypes.Message(nil), msgs...)
	if m.err != nil {
		return agenttypes.Message{}, m.err
	}
	return agenttypes.Message{Content: m.resp}, nil
}
func (m *mockChatLLM) ChatWithToolsStream(ctx context.Context, msgs []agenttypes.Message, tools []agenttypes.Tool) (<-chan agenttypes.StreamMessage, error) {
	return nil, nil
}
func (m *mockChatLLM) GetModelName() string { return "mock-model" }
func (m *mockChatLLM) GetModelMetadata() agenttypes.ModelMetadata {
	return agenttypes.ModelMetadata{Name: "mock-model"}
}

// addMsgs 便捷：向 provider 追加若干条消息。
func addMsgs(t *testing.T, p Provider, msgs []Message) {
	t.Helper()
	for _, m := range msgs {
		if err := p.AddMessage(context.Background(), m); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}
}

// ==================== NewHybrid 构造兜底 ====================

func TestNewHybrid_NilConfig(t *testing.T) {
	h := NewHybrid("s1", NewInMemory("s1", nil), nil, nil)
	if h == nil {
		t.Fatal("expected non-nil Hybrid")
	}
	if h.config == nil {
		t.Fatal("expected DefaultConfig fallback")
	}
	if !h.TailSummaryEnabled() {
		t.Fatal("Hybrid 应默认尾部注入")
	}
}

func TestNewHybrid_NoLLM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 2
	cfg.MaxRecentTailTokens = 2 // 让 u1 超预算留在 older，older 非空才有确定性摘要
	h := NewHybrid("s1", NewInMemory("s1", cfg), nil, cfg)
	// 无 LLM → Compress 走 DeterministicCompact fallback，不应 panic。
	addMsgs(t, h, []Message{
		{Role: "user", Content: "user one message"}, // ~5 tokens > budget 2
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
	})
	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if got, _ := h.GetSummary(context.Background()); got == "" {
		t.Fatal("无 LLM 时也应有 DeterministicCompact 摘要")
	}
}

// ==================== LLMSummaryAdapter ====================

func TestLLMSummaryAdapter_GenerateSummary(t *testing.T) {
	llm := &mockChatLLM{resp: "  摘要文本  "}
	a := NewLLMSummaryAdapter(llm)

	out, err := a.GenerateSummary(context.Background(), []Message{
		{Role: "user", Content: "你好"},
	})
	if err != nil {
		t.Fatalf("GenerateSummary: %v", err)
	}
	if out != "摘要文本" {
		t.Fatalf("expected trimmed content, got %q", out)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.lastMsgs) != 2 {
		t.Fatalf("expected system+1 message, got %d", len(llm.lastMsgs))
	}
	if llm.lastMsgs[0].Role != "system" || !strings.Contains(llm.lastMsgs[0].Content, "conversation compactor") {
		t.Fatalf("first message should be summary system prompt, got %q", llm.lastMsgs[0].Content)
	}
	if llm.lastMsgs[1].Content != "你好" {
		t.Fatalf("message content not passed through: %q", llm.lastMsgs[1].Content)
	}
}

func TestLLMSummaryAdapter_NilLLM(t *testing.T) {
	a := NewLLMSummaryAdapter(nil)
	out, err := a.GenerateSummary(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("nil LLM should not error, got %v", err)
	}
	if out != "" {
		t.Fatalf("nil LLM should return empty, got %q", out)
	}
}

func TestLLMSummaryAdapter_Error(t *testing.T) {
	llm := &mockChatLLM{err: errors.New("boom")}
	a := NewLLMSummaryAdapter(llm)
	if _, err := a.GenerateSummary(context.Background(), []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected error from LLM")
	}
}

// ==================== R1: 摘要输入消毒 ====================

func TestSanitizeSummaryInput_OrphanTools(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "call tool", ToolCalls: []agenttypes.ToolCall{{ID: "tc1"}}},
		{Role: "tool", Content: "result", ToolCallID: "tc1"},
		// 孤儿 tool（无前置 assistant tool_calls）→ 丢弃
		{Role: "tool", Content: "orphan", ToolCallID: "none"},
		// 孤儿 assistant tool_use（配对结果被切进 tail）→ 丢弃
		{Role: "assistant", Content: "orphan call", ToolCalls: []agenttypes.ToolCall{{ID: "tc2"}}},
	}
	out := sanitizeSummaryInput(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 sanitized messages, got %d: %+v", len(out), out)
	}
	if out[0].Role != "user" || out[1].Role != "assistant" || out[2].Role != "tool" {
		t.Fatalf("unexpected order: %+v", out)
	}
}

// ==================== R3: 输入条数上限 ====================

func TestCapSummaryInput(t *testing.T) {
	msgs := make([]Message, 10)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: "m"}
	}
	out := capSummaryInput(msgs, 3)
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	// 保留尾部（最近）消息
	if out[0].Content != "m" {
		t.Fatal("should keep tail messages")
	}
	if len(capSummaryInput(msgs, 0)) != 10 {
		t.Fatal("max<=0 应不裁剪")
	}
}

// ==================== Hybrid.Compress ====================

func TestHybrid_Compress_LLMSuccess(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 2
	cfg.MaxRecentTailTokens = 5
	base := NewInMemory("s1", cfg)
	llm := &mockSummaryLLM{summary: "LLM 摘要"}
	h := NewHybrid("s1", base, llm, cfg)

	addMsgs(t, h, []Message{
		{Role: "user", Content: strings.Repeat("x", 2000)}, // ~500 tokens，超预算 → older
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
	})

	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}

	// summary 为 LLM 输出
	got, _ := h.GetSummary(context.Background())
	if got != "LLM 摘要" {
		t.Fatalf("expected LLM summary, got %q", got)
	}

	// 尾部保留：tail = [u2,a2,u3,a3]（u2 被预算吸收，u1 超预算留在 older）
	msgs, err := base.GetMessages(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 tail messages, got %d", len(msgs))
	}
	if msgs[0].Content != "u2" || msgs[len(msgs)-1].Content != "a3" {
		t.Fatalf("tail 应为 u2..a3，got %+v", msgs)
	}

	// older = [big_u1, a1] 送 LLM（无前次摘要，不前置）
	llm.mu.Lock()
	if llm.calls != 1 {
		t.Fatalf("expected 1 GenerateSummary call, got %d", llm.calls)
	}
	last := llm.lastIn
	llm.mu.Unlock()
	if len(last) != 2 {
		t.Fatalf("expected 2 older messages (big_u1,a1), got %d: %+v", len(last), last)
	}
}

func TestHybrid_Compress_LLMFail_Fallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 1
	cfg.MaxRecentTailTokens = 5
	base := NewInMemory("s1", cfg)
	llm := &mockSummaryLLM{err: errors.New("timeout")}
	h := NewHybrid("s1", base, llm, cfg)

	addMsgs(t, h, []Message{
		{Role: "user", Content: strings.Repeat("x", 2000)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	})

	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	got, _ := h.GetSummary(context.Background())
	if got == "" {
		t.Fatal("fallback summary should be non-empty")
	}
	if !strings.Contains(got, "<scope>") {
		t.Fatalf("fallback should be DeterministicCompact output (contains <scope>), got %q", got)
	}
}

func TestHybrid_Compress_LLMEmpty_Fallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 1
	cfg.MaxRecentTailTokens = 5
	base := NewInMemory("s1", cfg)
	llm := &mockSummaryLLM{summary: "   "}
	h := NewHybrid("s1", base, llm, cfg)

	addMsgs(t, h, []Message{
		{Role: "user", Content: strings.Repeat("x", 2000)},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	})

	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	got, _ := h.GetSummary(context.Background())
	if got == "" {
		t.Fatalf("empty LLM output should fall back to deterministic, got %q", got)
	}
	if !strings.Contains(got, "<scope>") {
		t.Fatalf("fallback should be DeterministicCompact output, got %q", got)
	}
}

func TestHybrid_Compress_KeepRecentBoundary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	cfg.KeepRecentCount = 3
	base := NewInMemory("s1", cfg)
	llm := &mockSummaryLLM{summary: "LLM"}
	h := NewHybrid("s1", base, llm, cfg)

	addMsgs(t, h, []Message{{Role: "user", Content: "u1"}, {Role: "user", Content: "u2"}})
	if err := h.Compress(context.Background()); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()
	if calls != 0 {
		t.Fatalf("消息数 <= KeepRecentCount 时不应触发 LLM 摘要，got %d calls", calls)
	}
}

// ==================== 尾部原文保留 splitTailUserMessages ====================

func TestSplitTailUserMessages_MinKeep(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "u1"},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	// 预算极小（EstimateTokens("u1")=1，budget=1 仍吸收）→ 预算=1 不够，用 0 走
	// keepRecent 下限语义：tail 至少最近 2 条，且向前吸收直到预算耗尽。
	tail, older := splitTailUserMessages(msgs, 2, 1)
	if len(tail) < 2 {
		t.Fatalf("tail 至少含最近 2 条，got tail=%d", len(tail))
	}
	if len(tail)+len(older) != 3 {
		t.Fatalf("tail+older 应覆盖全部消息，got tail=%d older=%d", len(tail), len(older))
	}
	// 用超大预算验证「全部吸收」路径仍满足 keepRecent 下限。
	_, olderAll := splitTailUserMessages(msgs, 2, 100)
	if len(olderAll) != 0 {
		t.Fatalf("大预算应全吸收，got older=%d", len(olderAll))
	}
}

func TestSplitTailUserMessages_Budget(t *testing.T) {
	big := strings.Repeat("a", 4000) // ASCII ~1000 tokens
	small := "b"
	msgs := []Message{
		{Role: "user", Content: big},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: small},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	tail, older := splitTailUserMessages(msgs, 2, 1200)
	// 预算足够 → 全部吸收进 tail（older 为空）
	if len(older) != 0 {
		t.Fatalf("预算足够应全保留，got older=%d", len(older))
	}
	if len(tail) != 5 {
		t.Fatalf("expected tail=5, got %d", len(tail))
	}

	// 预算 500 → 吸收 small+u2+u3（~3 token），big(1000) 超预算停在其前。
	tail2, older2 := splitTailUserMessages(msgs, 2, 500)
	if len(older2) != 2 {
		t.Fatalf("expected older=2 (big,a1), got %d", len(older2))
	}
	if older2[0].Content != big || older2[1].Content != "a1" {
		t.Fatalf("older 应为 big,a1，got %+v", older2)
	}
	if tail2[0].Content != small {
		t.Fatalf("tail 应从 small 开始（切割点落在完整消息边界），got %+v", tail2)
	}
}

func TestSplitTailUserMessages_CJK(t *testing.T) {
	// 中文按 rune*2 计：10 rune → 20 tokens
	cjk := "中文中文中文中文中文"
	msgs := make([]Message, 0, 40)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, Message{Role: "user", Content: cjk})
	}
	// 预算 400 → 每条 CJK 消息 EstimateTokens = 10*2+1 = 21 tokens，
	// 400/21=19 条被吸收（19*21=399 ≤ 400）。
	tail, older := splitTailUserMessages(msgs, 2, 400)
	if len(older) != 19 {
		t.Fatalf("expected older=19 (40-2-19 absorbed), got %d", len(older))
	}
	if len(tail) != 21 {
		t.Fatalf("expected tail=21, got %d", len(tail))
	}
}

func TestSplitTailUserMessages_CompleteMessages(t *testing.T) {
	bigU := strings.Repeat("x", 2000) // ~500 tokens，超预算
	msgs := []Message{
		{Role: "user", Content: bigU},
		{Role: "assistant", Content: "call", ToolCalls: []agenttypes.ToolCall{{ID: "tc1"}}},
		{Role: "tool", Content: "r", ToolCallID: "tc1"},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}
	// 预算 50：u2/u3 可吸收；bigU 超预算停在 tool(tc1) 之后 → 切割点不能拦腰断 tool 配对。
	tail, older := splitTailUserMessages(msgs, 2, 50)
	if len(older) != 3 {
		t.Fatalf("expected older=3 (bigU..tool 完整), got %d", len(older))
	}
	if older[2].Role != "tool" || older[2].ToolCallID != "tc1" {
		t.Fatalf("切割点必须落在完整消息边界，tool 配对完整，got %+v", older)
	}
	if tail[0].Content != "u2" {
		t.Fatalf("tail 应从 u2 开始，got %+v", tail)
	}
}

// ==================== EstimateTokens 统一 ====================

func TestEstimateTokens_CJK(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"中文中文",
		"mixed 中 en 123",
	}
	for _, c := range cases {
		got := EstimateTokens(c)
		want := agenttypes.RoughTokenEstimate(c) + 1 // chatstore 比 agent/types 多 +1
		if got != want && c != "" {
			t.Fatalf("EstimateTokens(%q)=%d, want RoughTokenEstimate+1=%d", c, got, want)
		}
		if c == "" && got != 0 {
			t.Fatalf("EstimateTokens(\"\")=0, got %d", got)
		}
	}
}

// ==================== 单一摘要注入 ====================

func TestHybrid_GetMessages_SummaryTail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	base := NewInMemory("s1", cfg)
	h := NewHybrid("s1", base, nil, cfg)

	addMsgs(t, base, []Message{{Role: "user", Content: "u1"}})
	h.summaryMu.Lock()
	h.summary = "摘要X"
	h.summaryMu.Unlock()

	msgs, err := h.GetMessages(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (history + summary tail), got %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("summary 应为 user 消息，got role=%q", last.Role)
	}
	if !strings.HasPrefix(last.Content, SummaryMarker) || !strings.Contains(last.Content, "摘要X") {
		t.Fatalf("summary 应带 [Summary] marker，got %q", last.Content)
	}
}

func TestHybrid_GetMessages_NoSummary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMemoryCompress = false
	base := NewInMemory("s1", cfg)
	h := NewHybrid("s1", base, nil, cfg)

	addMsgs(t, base, []Message{{Role: "user", Content: "u1"}})
	msgs, err := h.GetMessages(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("无摘要时不追加，got %d messages", len(msgs))
	}
}
