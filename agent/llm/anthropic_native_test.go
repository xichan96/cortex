package llm

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// mockSSEBody builds an io.ReadCloser from raw SSE text.
func mockSSEBody(sse string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(sse))
}

// TestReadStreamBackfillsCacheUsage (U6) verifies message_start cache fields
// are backfilled into types.Usage using the B1 formula:
//   PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
//   TotalTokens  = PromptTokens + output_tokens
func TestReadStreamBackfillsCacheUsage(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":5,\"cache_read_input_tokens\":300,\"cache_creation_input_tokens\":200}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	if final.CachedTokens != 300 {
		t.Errorf("CachedTokens = %d, want 300", final.CachedTokens)
	}
	if final.CacheCreationTokens != 200 {
		t.Errorf("CacheCreationTokens = %d, want 200", final.CacheCreationTokens)
	}
	// B1: total prompt = uncached (100) + cache read (300) + cache creation (200) = 600
	if final.PromptTokens != 600 {
		t.Errorf("PromptTokens = %d, want 600 (total input incl. cache)", final.PromptTokens)
	}
	if final.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", final.CompletionTokens)
	}
	if final.TotalTokens != 605 {
		t.Errorf("TotalTokens = %d, want 605", final.TotalTokens)
	}
}

// TestReadStreamBackfillsReasoningTokens (P1.1) verifies reasoning_tokens is
// mapped into types.Usage.ReasoningTokens. It uses the native Anthropic wire
// shape — output_tokens_details.thinking_tokens as a subset breakdown, plus
// the OpenAI-compat reasoning_tokens alias some gateways emit — and asserts:
//   - ReasoningTokens is backfilled
//   - CompletionTokens/TotalTokens stay on output_tokens (reasoning is a billed
//     subset, not an additive term)
//   - message_delta usage is cumulative: the delta snapshot overwrites, it is
//     not added to the message_start seed (no double count)
func TestReadStreamBackfillsReasoningTokens(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":0,\"reasoning_tokens\":100,\"output_tokens_details\":{\"thinking_tokens\":80}}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"text\":\"think\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":20,\"reasoning_tokens\":150,\"output_tokens_details\":{\"thinking_tokens\":150}}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	// message_delta carries the cumulative snapshot (150); the message_start
	// seed (100 / 80) must not be added on top of it.
	if final.ReasoningTokens != 150 {
		t.Errorf("ReasoningTokens = %d, want 150 (cumulative delta snapshot)", final.ReasoningTokens)
	}
	// Reasoning is a subset of output_tokens: CompletionTokens/TotalTokens
	// keep output_tokens semantics, not output+reasoning.
	if final.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20 (output_tokens only)", final.CompletionTokens)
	}
	if final.TotalTokens != 70 {
		t.Errorf("TotalTokens = %d, want 70 (input 50 + output 20)", final.TotalTokens)
	}
}

// TestReadStreamReasoningTokensFromMessageStart covers the case where the
// reasoning count is only present on message_start (some gateways populate only
// the OpenAI-compat alias there and omit it from message_delta).
func TestReadStreamReasoningTokensFromMessageStart(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":0,\"reasoning_tokens\":120}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":30}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	if final.ReasoningTokens != 120 {
		t.Errorf("ReasoningTokens = %d, want 120 (from message_start)", final.ReasoningTokens)
	}
	if final.CompletionTokens != 30 {
		t.Errorf("CompletionTokens = %d, want 30", final.CompletionTokens)
	}
}

// TestReadStreamNoCacheFields ensures absence of cache fields degrades to zero
// and PromptTokens stays the plain input_tokens total.
func TestReadStreamNoCacheFields(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":10}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":10}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	if final.PromptTokens != 50 || final.TotalTokens != 60 {
		t.Errorf("PromptTokens/TotalTokens = %d/%d, want 50/60", final.PromptTokens, final.TotalTokens)
	}
	if final.CachedTokens != 0 || final.CacheCreationTokens != 0 {
		t.Errorf("cache fields should default to 0, got %d/%d", final.CachedTokens, final.CacheCreationTokens)
	}
}

// TestCollectStreamPassesUsage (U7) verifies collectStream forwards backfilled usage.
func TestCollectStreamPassesUsage(t *testing.T) {
	ch := make(chan types.StreamMessage, 3)
	ch <- types.StreamMessage{Type: "chunk", Content: "answer"}
	ch <- types.StreamMessage{Type: "end", Usage: &types.Usage{
		PromptTokens:        600,
		CompletionTokens:    5,
		TotalTokens:         605,
		CachedTokens:        300,
		CacheCreationTokens: 200,
	}}
	close(ch)

	msg, err := collectStream(ch)
	if err != nil {
		t.Fatalf("collectStream error: %v", err)
	}
	if msg.Usage.CachedTokens != 300 {
		t.Errorf("CachedTokens = %d, want 300", msg.Usage.CachedTokens)
	}
	if msg.Usage.CacheCreationTokens != 200 {
		t.Errorf("CacheCreationTokens = %d, want 200", msg.Usage.CacheCreationTokens)
	}
	if msg.Usage.PromptTokens != 600 {
		t.Errorf("PromptTokens = %d, want 600", msg.Usage.PromptTokens)
	}
}

// ---- Step 2: cache_control breakpoint injection (B2 budget ≤4 layout) ----

// buildTestMessages returns a messages list with nAssist assistant messages
// interleaved with user/tool_result messages, in the shape mergeConsecutiveRoles
// produces (block arrays).
func buildTestMessages(nAssist int) []types.Message {
	var msgs []types.Message
	for i := 0; i < nAssist; i++ {
		msgs = append(msgs, types.Message{
			Role:    "user",
			Content: "tool result for step",
		})
		msgs = append(msgs, types.Message{
			Role:    "assistant",
			Content: "step answer",
			ToolCalls: []types.ToolCall{{
				ID:   "call-step",
				Type: "function",
				Function: types.ToolFunction{Name: "tool_a", Arguments: map[string]interface{}{"q": i}},
			}},
		})
	}
	return msgs
}

// mockTool is a minimal types.Tool for buildRequest tests.
type mockTool struct{ name string }

func (m *mockTool) Name() string                         { return m.name }
func (m *mockTool) Description() string                  { return "mock tool " + m.name }
func (m *mockTool) Schema() map[string]interface{}       { return map[string]interface{}{"type": "object"} }
func (m *mockTool) Metadata() types.ToolMetadata         { return types.ToolMetadata{} }
func (m *mockTool) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func countBreakpoints(t *testing.T, body []byte) (system, tools, messages int) {
	t.Helper()
	var req struct {
		System json.RawMessage `json:"system"`
		Tools  []struct {
			CacheControl *anthropicCacheControl `json:"cache_control"`
		} `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.System) > 0 && string(req.System) != `""` && string(req.System) != "null" {
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(req.System, &blocks); err == nil {
			for _, b := range blocks {
				if b.CacheControl != nil {
					system++
				}
			}
		}
	}
	for _, tl := range req.Tools {
		if tl.CacheControl != nil {
			tools++
		}
	}
	for _, m := range req.Messages {
		// content may be a string or a block array; count breakpoints only in
		// block-array form (cache_control can only appear on blocks).
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			for _, b := range blocks {
				if b.CacheControl != nil {
					messages++
				}
			}
		}
	}
	return
}

// TestBuildRequestBreakpointsBudget (B2) constructs a request with >4 potential
// breakpoints (system + many tools + many assistant history messages) and
// asserts the total never exceeds MaxAnthropicCacheBreakpoints.
func TestBuildRequestBreakpointsBudget(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions() // Enabled, sys+tool+history budget 2
	p.promptCache.MinCacheTokens = 0                  // don't gate on short test messages

	msgs := buildTestMessages(8) // 8 assistant + 8 user = 16 history messages
	var tools []types.Tool
	for i := 0; i < 20; i++ {
		tools = append(tools, &mockTool{name: "tool_" + strconv.Itoa(i)})
	}
	msgs = append([]types.Message{{Role: "system", Content: "You are a helpful assistant."}}, msgs...)

	body, err := p.buildRequest(msgs, tools, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	sys, tl, hist := countBreakpoints(t, body)
	total := sys + tl + hist
	if total > types.MaxAnthropicCacheBreakpoints {
		t.Fatalf("breakpoints %d (sys=%d tools=%d hist=%d) exceed cap %d", total, sys, tl, hist, types.MaxAnthropicCacheBreakpoints)
	}
	// Default layout: system 1 + last tool 1 + history ≤2 = exactly 4.
	if total != 4 {
		t.Errorf("breakpoints = %d (sys=%d tools=%d hist=%d), want exactly 4", total, sys, tl, hist)
	}
	if sys != 1 {
		t.Errorf("system breakpoints = %d, want 1", sys)
	}
	if tl != 1 {
		t.Errorf("tool breakpoints = %d, want 1 (only last tool)", tl)
	}
	if hist != 2 {
		t.Errorf("history breakpoints = %d, want 2 (budget)", hist)
	}
}

// TestBuildRequestSystemBreakpoint (U1) verifies system becomes a blocks array
// with cache_control when caching is enabled.
func TestBuildRequestSystemBreakpoint(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.HistoryEveryN = 0 // isolate the system breakpoint

	msgs := []types.Message{{Role: "system", Content: "SYS"}, {Role: "user", Content: "hi"}}
	body, err := p.buildRequest(msgs, nil, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	var req struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.System, &blocks); err != nil {
		t.Fatalf("system should be a blocks array, raw=%s err=%v", string(req.System), err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "SYS" {
		t.Fatalf("unexpected system blocks: %+v", blocks)
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("system block should carry ephemeral cache_control")
	}
}

// TestBuildRequestLastToolBreakpoint (U2) verifies only the last tool carries
// cache_control.
func TestBuildRequestLastToolBreakpoint(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.HistoryEveryN = 0
	p.promptCache.SystemBreakpoint = false

	msgs := []types.Message{{Role: "user", Content: "hi"}}
	var tools []types.Tool
	for i := 0; i < 5; i++ {
		tools = append(tools, &mockTool{name: "tool_" + strconv.Itoa(i)})
	}
	body, err := p.buildRequest(msgs, tools, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	_, tl, _ := countBreakpoints(t, body)
	if tl != 1 {
		t.Errorf("tool breakpoints = %d, want exactly 1 (last tool)", tl)
	}
}

// TestBuildRequestHistoryBudget (U3) verifies history breakpoints land only on
// assistant last-blocks and honor the budget.
func TestBuildRequestHistoryBudget(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.SystemBreakpoint = false
	p.promptCache.ToolsBreakpoint = false
	p.promptCache.HistoryEveryN = 2
	p.promptCache.MinCacheTokens = 0

	msgs := buildTestMessages(8)
	body, err := p.buildRequest(msgs, nil, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	_, _, hist := countBreakpoints(t, body)
	if hist != 2 {
		t.Errorf("history breakpoints = %d, want 2 (budget)", hist)
	}

	// No breakpoint may land on a tool_result block: tool_result blocks belong
	// to user messages. Verify each marked block type is not tool_result.
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range req.Messages {
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue // string content
		}
		for _, b := range blocks {
			if b.CacheControl != nil && b.Type == "tool_result" {
				t.Errorf("cache_control must not be placed on tool_result block")
			}
		}
	}
}

// TestBuildRequestHistoryBelowMinTokens (U5) verifies short history produces no
// history breakpoints.
func TestBuildRequestHistoryBelowMinTokens(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.SystemBreakpoint = false
	p.promptCache.ToolsBreakpoint = false
	p.promptCache.HistoryEveryN = 2
	p.promptCache.MinCacheTokens = 1 << 20 // huge → history always below threshold

	msgs := buildTestMessages(2)
	body, err := p.buildRequest(msgs, nil, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	_, _, hist := countBreakpoints(t, body)
	if hist != 0 {
		t.Errorf("history breakpoints = %d, want 0 below MinCacheTokens", hist)
	}
}

// TestBuildRequestDisabledByteCompat (U4) verifies Enabled=false leaves the
// request byte-identical to a pre-caching baseline (no cache_control anywhere,
// system stays a plain string).
func TestBuildRequestDisabledByteCompat(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.Enabled = false

	msgs := buildTestMessages(6)
	msgs = append([]types.Message{{Role: "system", Content: "SYS"}, {Role: "user", Content: "hi"}}, msgs...)
	var tools []types.Tool
	for i := 0; i < 5; i++ {
		tools = append(tools, &mockTool{name: "tool_" + strconv.Itoa(i)})
	}

	body, err := p.buildRequest(msgs, tools, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	var req struct {
		System   string                   `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.System != "SYS" {
		t.Errorf("system should remain a plain string when disabled, got %q", req.System)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Error("request body must not contain cache_control when disabled")
	}
	// Every message content is a block array (merge) or string; assert no
	// cache_control key anywhere in the JSON.
	for _, m := range req.Messages {
		_ = m.Content
	}
}

// TestBuildRequestHistoryBudgetOverflow (B2) verifies that even when
// HistoryEveryN is huge, the total stays ≤4 (hard cap enforced).
func TestBuildRequestHistoryBudgetOverflow(t *testing.T) {
	p := &NativeAnthropicProvider{model: "claude-test", maxTokens: 100}
	p.promptCache = types.DefaultPromptCacheOptions()
	p.promptCache.HistoryEveryN = 100   // pathological
	p.promptCache.MinCacheTokens = 0    // don't gate on short test messages

	msgs := buildTestMessages(30)
	msgs = append([]types.Message{{Role: "system", Content: "SYS"}, {Role: "user", Content: "hi"}}, msgs...)
	var tools []types.Tool
	for i := 0; i < 20; i++ {
		tools = append(tools, &mockTool{name: "tool_" + strconv.Itoa(i)})
	}
	body, err := p.buildRequest(msgs, tools, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	sys, tl, hist := countBreakpoints(t, body)
	total := sys + tl + hist
	if total > types.MaxAnthropicCacheBreakpoints {
		t.Fatalf("breakpoints %d exceed hard cap %d", total, types.MaxAnthropicCacheBreakpoints)
	}
}

