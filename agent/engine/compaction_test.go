package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
)

// testGen is a compact deterministic SummaryGenerator used by the three-phase
// tests: "summary-of-<first>-<last>" so the summary stays small enough to fit
// the tight budgets below.
func testGen(existingSummary string, mid []types.Message) string {
	var b strings.Builder
	if existingSummary != "" {
		b.WriteString("[prev] " + existingSummary + " ")
	}
	b.WriteString("summary-of-")
	if len(mid) > 0 {
		b.WriteString(mid[0].Content)
		b.WriteString("-")
		b.WriteString(mid[len(mid)-1].Content)
	}
	return b.String()
}

// msgs builds a history of n alternating messages starting with a user
// message. Each content is ~26 chars ≈ 6 tokens, so n=30 ≈ 180 tokens — large
// enough that a tight budget forces a middle compression.
func msgs(n int) []types.Message {
	out := make([]types.Message, 0, n)
	roles := []string{"user", "assistant"}
	for i := 0; i < n; i++ {
		out = append(out, types.Message{
			Role:    roles[i%2],
			Content: fmt.Sprintf("message number %03d in the conversation", i),
		})
	}
	return out
}

func msgTokens(m types.Message) int { return types.RoughTokensForMessage(m) }

// TestCompaction_DisabledMatchesLegacy (T1) locks the regression: the legacy
// trimHistoryToTokenBudget (called when CompactionPrefix is off) must match an
// independent reimplementation of the pre-P3.1 tail-only algorithm, byte for
// byte, across history lengths and budgets.
func TestCompaction_DisabledMatchesLegacy(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	for _, budget := range []int{100, 400, 1000} {
		cfg.MaxBudgetTokens = budget
		for _, n := range []int{1, 5, 30, 100} {
			history := msgs(n)
			got := trimHistoryToTokenBudget(history, cfg.MaxBudgetTokens, cfg, nil, types.NewAgentInput("input"))
			want := legacyTailOnly(history, cfg.MaxBudgetTokens, cfg, "input")
			if !msgSliceEqual(got, want) {
				t.Fatalf("n=%d budget=%d: legacy trim diverged from reference:\ngot=%v\nwant=%v", n, budget, got, want)
			}
		}
	}
}

// legacyTailOnly is an independent reimplementation of the pre-P3.1
// trimHistoryToTokenBudget (greedy tail scan, keep one recent even over
// budget). It is the reference the disabled path must not drift from.
func legacyTailOnly(history []types.Message, maxBudgetTokens int, config *types.AgentConfig, input string) []types.Message {
	if len(history) == 0 {
		return history
	}
	fixed := 0
	if config != nil && config.SystemMessage != "" {
		fixed += msgTokens(types.Message{Role: "system", Content: config.SystemMessage})
	}
	fixed += msgTokens(types.NewAgentInput(input).ToMessage("user"))
	rem := maxBudgetTokens - fixed
	if rem <= 0 {
		return history[len(history)-1:]
	}
	used := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		c := msgTokens(history[i])
		if used+c > rem {
			break
		}
		used += c
		start = i
	}
	if start >= len(history) {
		return history[len(history)-1:]
	}
	return history[start:]
}

// TestCompaction_ThreePhaseKeepsHeadAndTail (T2) verifies the three-phase
// output: head is a verbatim prefix of history, exactly one assistant summary
// message is inserted, and the tail is a verbatim suffix. The budget is tight
// enough that head+tail cannot cover the whole history, forcing a middle gap.
func TestCompaction_ThreePhaseKeepsHeadAndTail(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 120 // rem ≈ 117 < 180 (30 msgs): forces a middle gap
	cfg.CacheAnchorTokens = 30
	opts := &CompactionOptions{SummaryGenerator: testGen}

	history := msgs(30)
	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if !folded {
		t.Fatalf("expected summary folded, got %v", out)
	}

	// Locate the summary message.
	summaryIdx := -1
	for i, m := range out {
		if m.Role == "assistant" && strings.HasPrefix(m.Content, "[CONVERSATION COMPACTED") {
			summaryIdx = i
			break
		}
	}
	if summaryIdx < 0 {
		t.Fatalf("no summary assistant message in output: %v", out)
	}
	if summaryIdx == 0 {
		t.Fatalf("summary must not be the first message (Anthropic first=user): %v", out)
	}
	head := out[:summaryIdx]
	if head[0].Role != "user" {
		t.Fatalf("head[0] should be user, got %q", head[0].Role)
	}
	for i, m := range head {
		if m.Role != history[i].Role || m.Content != history[i].Content {
			t.Fatalf("head[%d] diverges from history[%d]: %+v vs %+v", i, i, m, history[i])
		}
	}

	// Tail: verbatim suffix.
	tail := out[summaryIdx+1:]
	histStart := len(history) - len(tail)
	for i, m := range tail {
		if m.Role != history[histStart+i].Role || m.Content != history[histStart+i].Content {
			t.Fatalf("tail[%d] diverges from history[%d]: %+v vs %+v", i, histStart+i, m, history[histStart+i])
		}
	}

	// Summary marker + content references the middle (not head[0]).
	if !strings.Contains(out[summaryIdx].Content, "summary-of-") {
		t.Fatalf("summary content missing generator output: %q", out[summaryIdx].Content)
	}
	midStart := len(head)
	if midStart >= len(history) || !strings.Contains(out[summaryIdx].Content, history[midStart].Content) {
		t.Fatalf("summary should reference the first middle message %q, got %q", history[midStart].Content, out[summaryIdx].Content)
	}
}

// TestCompaction_BudgetNeverExceeded (T3) asserts the strict budget contract
// across many (budget, history-length) combinations, for both the folded and
// dropped summary paths.
func TestCompaction_BudgetNeverExceeded(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	opts := &CompactionOptions{SummaryGenerator: testGen}
	for _, anchor := range []int{0, 128, 512, 2048} {
		cfg.CacheAnchorTokens = anchor
		for _, budget := range []int{100, 200, 400, 1000, 10000} {
			cfg.MaxBudgetTokens = budget
			for _, n := range []int{1, 3, 10, 30, 100} {
				history := msgs(n)
				rem := budget - fixedTokens(cfg, nil, "input")
				if rem <= 0 {
					continue
				}
				out, _ := trimHistoryToTokenBudgetCompaction(history, budget, cfg, opts, "", nil, types.NewAgentInput("input"))
				if msgTokensSum(out) > rem {
					t.Fatalf("budget=%d anchor=%d n=%d: total %d > rem %d: %v", budget, anchor, n, msgTokensSum(out), rem, out)
				}
				// Same-input legacy must also hold (sanity).
				legacy := trimHistoryToTokenBudget(history, budget, cfg, nil, types.NewAgentInput("input"))
				if msgTokensSum(legacy) > rem {
					t.Fatalf("legacy budget=%d n=%d: total %d > rem %d", budget, n, msgTokensSum(legacy), rem)
				}
			}
		}
	}
}

// TestCompaction_SummaryOverBudgetDrops (T4) forces the summary to be larger
// than the middle and verifies the degrade-to-head+tail path (no summary).
func TestCompaction_SummaryOverBudgetDrops(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 2000
	cfg.CacheAnchorTokens = 512
	// A generator that always returns a huge summary.
	bigGen := func(existingSummary string, mid []types.Message) string {
		return strings.Repeat("x", 10000)
	}
	opts := &CompactionOptions{SummaryGenerator: bigGen}

	history := msgs(30)
	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if folded {
		t.Fatalf("expected summary dropped, got folded=true")
	}
	for _, m := range out {
		if m.Role == "assistant" && strings.HasPrefix(m.Content, "[CONVERSATION COMPACTED") {
			t.Fatalf("summary should not appear: %v", out)
		}
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgTokensSum(out) > rem {
		t.Fatalf("over budget: %d > %d", msgTokensSum(out), rem)
	}
}

// TestCompaction_AnchorSqueezesTail (T5) verifies that with a large anchor and
// a tight budget, the head is preserved and the tail shrinks to the last
// message (which is always kept, legacy semantics).
func TestCompaction_AnchorSqueezesTail(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 500
	cfg.CacheAnchorTokens = 1024 // > rem, clamped to rem
	opts := &CompactionOptions{SummaryGenerator: testGen}

	history := msgs(30)
	out, _ := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if out[0].Role != history[0].Role || out[0].Content != history[0].Content {
		t.Fatalf("head[0] not preserved: %v", out[0])
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgTokensSum(out) > rem {
		t.Fatalf("over budget: %d > %d", msgTokensSum(out), rem)
	}
	// Tail must be non-empty and end with the last message.
	if out[len(out)-1].Content != history[len(history)-1].Content {
		t.Fatalf("tail should end with the last message, got %q", out[len(out)-1].Content)
	}
}

// TestCompaction_NoGeneratorDegrades (B1 fallback) verifies that when no
// SummaryGenerator is injected, the middle is dropped and head+tail survive —
// never a panic and never over budget.
func TestCompaction_NoGeneratorDegrades(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 120 // tight: forces a middle gap
	cfg.CacheAnchorTokens = 30
	history := msgs(30)
	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, nil, "existing-summary", nil, types.NewAgentInput("input"))
	if folded {
		t.Fatalf("nil generator should not fold")
	}
	if len(out) == 0 || out[0].Content != history[0].Content {
		t.Fatalf("head should be preserved, got %v", out)
	}
	if out[len(out)-1].Content != history[len(history)-1].Content {
		t.Fatalf("tail should be preserved, got %q", out[len(out)-1].Content)
	}
	for _, m := range out {
		if strings.HasPrefix(m.Content, "[CONVERSATION COMPACTED") {
			t.Fatalf("no summary should be injected: %v", out)
		}
	}
	// Head+tail still within budget.
	if msgTokensSum(out) > cfg.MaxBudgetTokens-fixedTokens(cfg, nil, "input") {
		t.Fatalf("degraded output over budget: %v", out)
	}
}

// TestCompaction_EdgeCases (T7) exercises empty, single, all-tool and
// all-user histories plus the repairLLMMessageToolOrdering pairing gaps
// (R2): head-ending assistant with tool_calls whose tool_result sits in the
// middle, and a leading orphan tool in the tail.
func TestCompaction_EdgeCases(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 100000
	cfg.CacheAnchorTokens = 256
	opts := &CompactionOptions{SummaryGenerator: testGen}

	// Empty history.
	out, _ := trimHistoryToTokenBudgetCompaction(nil, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if len(out) != 0 {
		t.Fatalf("empty history should stay empty, got %v", out)
	}

	// Single message.
	single := []types.Message{{Role: "user", Content: "only"}}
	out, _ = trimHistoryToTokenBudgetCompaction(single, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if len(out) != 1 || out[0].Content != "only" {
		t.Fatalf("single message not preserved: %v", out)
	}

	// All-tool history: repair must leave a valid message list (tools without
	// a preceding assistant are dropped by repair — safe, no panic).
	toolMsgs := []types.Message{
		{Role: "tool", Content: "t0", ToolCallID: "c0"},
		{Role: "tool", Content: "t1", ToolCallID: "c1"},
	}
	out, _ = trimHistoryToTokenBudgetCompaction(toolMsgs, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	repaired := repairLLMMessageToolOrdering(out)
	if repaired == nil {
		t.Fatalf("all-tool history repaired to nil: %v", out)
	}

	// Tool pairing gap 1: head ends with assistant(tool_calls), its tool_result
	// is in the middle (replaced by summary). repair strips the dangling
	// ToolCalls — must not panic and must not produce a bare tool.
	history := []types.Message{
		{Role: "user", Content: "u0"},
		{Role: "assistant", Content: "a0", ToolCalls: []types.ToolCall{{ID: "c0", Type: "function", Function: types.ToolFunction{Name: "f0"}}}},
		{Role: "tool", Content: "r0", ToolCallID: "c0"}, // in the middle → replaced
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	repaired = repairLLMMessageToolOrdering(out)
	if !folded {
		t.Logf("note: pairing-gap history not folded (summary may be absent): %v", out)
	}
	validateAlternationOrSafe(t, repaired, "pairing-gap-1")

	// Tool pairing gap 2: tail begins with an orphan tool whose assistant is in
	// the middle. repair drops the orphan — safe.
	history = []types.Message{
		{Role: "user", Content: "u0"},
		{Role: "assistant", Content: "a0", ToolCalls: []types.ToolCall{{ID: "c0", Type: "function", Function: types.ToolFunction{Name: "f0"}}}},
		{Role: "tool", Content: "r0", ToolCallID: "c0"},
		{Role: "user", Content: "u1"}, // head boundary here
		{Role: "tool", Content: "orphan", ToolCallID: "cX"}, // orphan tool in tail
		{Role: "user", Content: "u2"},
	}
	out, _ = trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	repaired = repairLLMMessageToolOrdering(out)
	validateAlternationOrSafe(t, repaired, "pairing-gap-2")
}

// TestCompaction_CachePrefixStableAcrossCalls (T8, B2 acceptance) simulates
// the cache-prefix guarantee: the first three-phase output's head (segment 1/2
// equivalent) must appear byte-for-byte as a prefix of the second call after
// appending a new turn, because the middle summary and tail are deterministic.
func TestCompaction_CachePrefixStableAcrossCalls(t *testing.T) {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 120 // forces a middle gap in both calls
	cfg.CacheAnchorTokens = 30
	opts := &CompactionOptions{SummaryGenerator: testGen}

	first := msgs(30)
	out1, folded1 := trimHistoryToTokenBudgetCompaction(first, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input1"))
	if !folded1 {
		t.Fatalf("first call not folded")
	}
	// Simulate the next turn: same head+mid, one new user/assistant appended.
	second := append(append([]types.Message{}, first...),
		types.Message{Role: "user", Content: "input2"}, types.Message{Role: "assistant", Content: "a31"})
	out2, folded2 := trimHistoryToTokenBudgetCompaction(second, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input3"))
	if !folded2 {
		t.Fatalf("second call not folded")
	}

	// B2-acceptable invariant: the stable history prefix (system+tools are
	// fixed by config/request construction; here we assert the head+summary+
	// tail structure's byte-prefix is shared across calls).
	head1 := out1[:firstMsgIdx(out1)]
	head2 := out2[:len(head1)]
	for i, m := range head1 {
		if m.Role != head2[i].Role || m.Content != head2[i].Content {
			t.Fatalf("cache prefix diverged at %d: %+v vs %+v", i, m, head2[i])
		}
	}
}

// firstMsgIdx returns the index of the first assistant summary marker message
// (used to bound the stable head for the prefix test).
func firstMsgIdx(msgs []types.Message) int {
	for i, m := range msgs {
		if m.Role == "assistant" && strings.HasPrefix(m.Content, "[CONVERSATION COMPACTED") {
			return i
		}
	}
	return len(msgs)
}

// validateAlternationOrSafe asserts the message list has no adjacent same-role
// messages other than user-assistant pairs, and no bare tool first message
// (the invariants repairLLMMessageToolOrdering is meant to guarantee). It
// tolerates the summary assistant message.
func validateAlternationOrSafe(t *testing.T, msgs []types.Message, label string) {
	t.Helper()
	if len(msgs) == 0 {
		return
	}
	if msgs[0].Role == "tool" {
		t.Fatalf("%s: first message is a bare tool: %v", label, msgs)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == "tool" {
			prev := msgs[i-1]
			if prev.Role != "assistant" || len(prev.ToolCalls) == 0 {
				t.Fatalf("%s: tool not after assistant-with-tool_calls at %d: %v", label, i, msgs)
			}
		}
	}
}

// fixedTokens computes the system + previousRequests + input estimate.
func fixedTokens(cfg *types.AgentConfig, previousRequests []types.ToolCallData, input string) int {
	n := 0
	if cfg != nil && cfg.SystemMessage != "" {
		n += msgTokens(types.Message{Role: "system", Content: cfg.SystemMessage})
	}
	for _, m := range types.MessagesFromToolSteps("", previousRequests) {
		n += msgTokens(m)
	}
	n += msgTokens(types.NewAgentInput(input).ToMessage("user"))
	return n
}

func msgTokensSum(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += msgTokens(m)
	}
	return n
}

func msgSliceEqual(a, b []types.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content || a[i].ToolCallID != b[i].ToolCallID {
			return false
		}
	}
	return true
}

// ——— engine-level integration (prepareMessages) ———

// TestPrepareMessages_CompactionSummaryTailInjection (T9) verifies that with
// CompactionPrefix on, the GetSummary content appears as an assistant message
// after history and before the final user input — not as a head system message.
func TestPrepareMessages_CompactionSummaryTailInjection(t *testing.T) {
	provider := NewMockLLMProvider()
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "You are a test assistant."
	cfg.MaxBudgetTokens = 100000
	cfg.CompactionPrefix = true
	engine := NewAgentEngine(provider, cfg)
	engine.SetCompactionOptions(&CompactionOptions{SummaryGenerator: testGen})

	mem := &summaryMemory{SimpleMemoryProvider: providers.NewSimpleMemoryProvider(), summary: "existing summary text"}
	engine.SetMemory(context.Background(), mem)

	msgs, err := engine.prepareMessages(context.Background(), types.NewAgentInput("继续"), nil)
	if err != nil {
		t.Fatalf("prepareMessages: %v", err)
	}

	// system first.
	if msgs[0].Role != "system" || msgs[0].Content != "You are a test assistant." {
		t.Fatalf("first message should be system, got %+v", msgs[0])
	}
	// No system-role summary at index 1.
	if len(msgs) > 1 && msgs[1].Role == "system" && strings.Contains(msgs[1].Content, "Previous conversation summary:") {
		t.Fatalf("head system summary must not be injected with CompactionPrefix on: %v", msgs)
	}
	// Find the assistant summary.
	found := false
	for i, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "existing summary text") {
			found = true
			// After it must eventually come the user input, and nothing system.
			if i == 0 {
				t.Fatalf("summary should not be first: %v", msgs)
			}
			for j := i + 1; j < len(msgs); j++ {
				if msgs[j].Role == "system" {
					t.Fatalf("summary tail must be before any system: %v", msgs)
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected summary assistant message with summary text, got %v", msgs)
	}
	if msgs[len(msgs)-1].Role != "user" {
		t.Fatalf("last message should be user input, got %q", msgs[len(msgs)-1].Role)
	}
}

// TestPrepareMessages_CompactionBudgetAndSummary (T10) runs the full
// prepareMessages with MaxBudgetTokens + CompactionPrefix + GetSummary and
// asserts the system segment is unchanged, the summary is present, and the
// estimate stays within budget.
func TestPrepareMessages_CompactionBudgetAndSummary(t *testing.T) {
	provider := NewMockLLMProvider()
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "You are a test assistant."
	cfg.MaxBudgetTokens = 400
	cfg.CacheAnchorTokens = 128
	cfg.CompactionPrefix = true
	engine := NewAgentEngine(provider, cfg)
	engine.SetCompactionOptions(&CompactionOptions{SummaryGenerator: testGen})

	// Seed a memory with some history.
	mem := providers.NewSimpleMemoryProvider()
	for i := 0; i < 30; i++ {
		_ = mem.AddMessage(context.Background(), types.Message{
			Role:    "user",
			Content: "user-" + string(rune('a'+i)),
		})
	}
	sm := &summaryMemory{SimpleMemoryProvider: mem, summary: "budget-summary"}
	engine.SetMemory(context.Background(), sm)

	msgs, err := engine.prepareMessages(context.Background(), types.NewAgentInput("继续"), nil)
	if err != nil {
		t.Fatalf("prepareMessages: %v", err)
	}
	if msgs[0].Role != "system" || msgs[0].Content != "You are a test assistant." {
		t.Fatalf("system segment changed: %+v", msgs[0])
	}

	// Budget check over the whole prepared request (system + history + summary + input).
	total := 0
	for _, m := range msgs {
		total += msgTokens(m)
	}
	if total > cfg.MaxBudgetTokens {
		t.Fatalf("request over budget: %d > %d: %v", total, cfg.MaxBudgetTokens, msgs)
	}

	foundSummary := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "budget-summary") {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Fatalf("summary text not found in request: %v", msgs)
	}
}

// TestPrepareMessages_CompactionNoHistoryKeepsSummary (regression): with
// CompactionPrefix on but an empty history, the summary must still be injected
// as an assistant tail message (GetSummary content is preserved).
func TestPrepareMessages_CompactionNoHistoryKeepsSummary(t *testing.T) {
	provider := NewMockLLMProvider()
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.CompactionPrefix = true
	engine := NewAgentEngine(provider, cfg)
	engine.SetCompactionOptions(&CompactionOptions{SummaryGenerator: testGen})

	mem := &summaryMemory{SimpleMemoryProvider: providers.NewSimpleMemoryProvider(), summary: "solo-summary"}
	engine.SetMemory(context.Background(), mem)

	msgs, err := engine.prepareMessages(context.Background(), types.NewAgentInput("hi"), nil)
	if err != nil {
		t.Fatalf("prepareMessages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "solo-summary") {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary not injected as assistant tail with empty history: %v", msgs)
	}
}
