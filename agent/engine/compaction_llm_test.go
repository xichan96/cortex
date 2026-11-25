package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// llmTestConfig builds the tight budget shared by the LLM summary tests:
// rem = 118, head anchor = 27 tokens (3 messages), leaving 91 tokens for
// summary + tail.
func llmTestConfig() *types.AgentConfig {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.MaxBudgetTokens = 120
	cfg.CacheAnchorTokens = 30
	return cfg
}

// llmRecorder records every LLM summary request so tests can assert call count,
// the existing summary folded in, and the delta that was summarized.
type llmRecorder struct {
	calls    int
	existing []string
	mids     [][]types.Message
	reply    func(call int) (string, error)
}

func (r *llmRecorder) gen(_ context.Context, existing string, mid []types.Message) (string, error) {
	r.calls++
	r.existing = append(r.existing, existing)
	r.mids = append(r.mids, append([]types.Message{}, mid...))
	if r.reply != nil {
		return r.reply(r.calls)
	}
	return fmt.Sprintf("llm-summary-%d", r.calls), nil
}

// TestCompactionLLM_UsesModelSummary checks the basic shape: one model call,
// assistant summary with the marker, verbatim head/tail, strict budget.
func TestCompactionLLM_UsesModelSummary(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	var cache compactionSummaryCache

	history := msgs(30)
	out, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded {
		t.Fatalf("expected folded summary, got %v", out)
	}
	if rec.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1", rec.calls)
	}

	summaryIdx := -1
	for i, m := range out {
		if m.Role == "assistant" && strings.HasPrefix(m.Content, "[CONVERSATION COMPACTED") {
			summaryIdx = i
			break
		}
	}
	if summaryIdx < 0 {
		t.Fatalf("no summary message: %v", out)
	}
	if !strings.Contains(out[summaryIdx].Content, "llm-summary-1") {
		t.Fatalf("summary does not carry model output: %q", out[summaryIdx].Content)
	}
	for i := 0; i < summaryIdx; i++ {
		if out[i].Role != history[i].Role || out[i].Content != history[i].Content {
			t.Fatalf("head[%d] diverged: %+v vs %+v", i, out[i], history[i])
		}
	}
	tail := out[summaryIdx+1:]
	histStart := len(history) - len(tail)
	for i, m := range tail {
		if m.Role != history[histStart+i].Role || m.Content != history[histStart+i].Content {
			t.Fatalf("tail[%d] diverged: %+v vs %+v", i, m, history[histStart+i])
		}
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgTokensSum(out) > rem {
		t.Fatalf("over budget: %d > %d", msgTokensSum(out), rem)
	}
}

// TestCompactionLLM_CacheReusesSummary verifies the prompt-cache-stable steady
// state: the same history does not call the model twice and produces the same
// summary message byte-for-byte.
func TestCompactionLLM_CacheReusesSummary(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	var cache compactionSummaryCache
	history := msgs(30)

	out1, folded1 := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	out2, folded2 := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded1 || !folded2 {
		t.Fatalf("expected both trims folded: %v / %v", folded1, folded2)
	}
	if rec.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1 (cached summary reused)", rec.calls)
	}
	if !msgSliceEqual(out1, out2) {
		t.Fatalf("cached output diverged:\n%v\n%v", out1, out2)
	}
}

// TestCompactionLLM_ReusesWhileDeltaFitsTail appends a few messages that still
// fit in the tail budget: the cached summary must be reused verbatim (stable
// prompt prefix, no extra model call) and the result must stay in budget.
func TestCompactionLLM_ReusesWhileDeltaFitsTail(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	var cache compactionSummaryCache

	history := msgs(30)
	if _, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache); !folded {
		t.Fatalf("first trim not folded")
	}
	grown := append(append([]types.Message{}, history...),
		types.Message{Role: "user", Content: "new user turn"},
		types.Message{Role: "assistant", Content: "new assistant turn"},
	)
	out, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), grown, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded {
		t.Fatalf("second trim not folded: %v", out)
	}
	if rec.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1 (delta fit the tail, no re-summary)", rec.calls)
	}
	found := false
	for _, m := range out {
		if strings.Contains(m.Content, "llm-summary-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("cached summary missing from output: %v", out)
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgTokensSum(out) > rem {
		t.Fatalf("over budget: %d > %d", msgTokensSum(out), rem)
	}
}

// TestCompactionLLM_RegeneratesDeltaOnly grows the history past the tail budget:
// the engine must summarize only the uncovered delta, folding the cached
// summary in as existingSummary rather than re-sending summarized messages.
func TestCompactionLLM_RegeneratesDeltaOnly(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	var cache compactionSummaryCache

	history := msgs(30)
	firstOut, _ := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if rec.calls != 1 {
		t.Fatalf("first call count = %d, want 1", rec.calls)
	}
	firstSummary := ""
	for _, m := range firstOut {
		if strings.Contains(m.Content, "llm-summary-1") {
			firstSummary = m.Content
		}
	}
	if firstSummary == "" {
		t.Fatalf("first summary missing: %v", firstOut)
	}

	grown := msgs(30)
	for i := 0; i < 15; i++ {
		grown = append(grown, types.Message{Role: "user", Content: fmt.Sprintf("grown message %02d", i)})
	}
	out, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), grown, cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded {
		t.Fatalf("grown trim not folded: %v", out)
	}
	if rec.calls != 2 {
		t.Fatalf("LLM calls = %d, want 2", rec.calls)
	}
	if !strings.Contains(rec.existing[1], "llm-summary-1") {
		t.Fatalf("delta summary did not fold the cached summary: %q", rec.existing[1])
	}
	if len(rec.mids[1]) >= len(grown) {
		t.Fatalf("delta passed to LLM has %d messages; expected only uncovered messages", len(rec.mids[1]))
	}
	foundNew := false
	for _, m := range out {
		if strings.Contains(m.Content, "llm-summary-2") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("new summary missing from output: %v", out)
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgTokensSum(out) > rem {
		t.Fatalf("over budget: %d > %d", msgTokensSum(out), rem)
	}
}

// TestCompactionLLM_ErrorFallsBackToDeterministic guards the availability
// contract: a failed model call must degrade to the injected deterministic
// generator, not to "drop the middle silently".
func TestCompactionLLM_ErrorFallsBackToDeterministic(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{reply: func(int) (string, error) { return "", errors.New("model down") }}
	opts := &CompactionOptions{
		SummaryGenerator:    testGen,
		LLMSummaryGenerator: rec.gen,
	}
	var cache compactionSummaryCache

	out, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), msgs(30), cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded {
		t.Fatalf("deterministic fallback did not fold: %v", out)
	}
	if rec.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1", rec.calls)
	}
	found := false
	for _, m := range out {
		if strings.Contains(m.Content, "summary-of-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("deterministic summary missing: %v", out)
	}
}

// TestCompactionLLM_EmptyReplyFallsBack treats a blank model reply like a
// failure (the adapter returns "" without an error in some paths).
func TestCompactionLLM_EmptyReplyFallsBack(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{reply: func(int) (string, error) { return "   \n", nil }}
	opts := &CompactionOptions{
		SummaryGenerator:    testGen,
		LLMSummaryGenerator: rec.gen,
	}
	var cache compactionSummaryCache

	out, folded := trimHistoryToTokenBudgetCompactionCtx(
		context.Background(), msgs(30), cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !folded {
		t.Fatalf("deterministic fallback did not fold: %v", out)
	}
	found := false
	for _, m := range out {
		if strings.Contains(m.Content, "summary-of-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("deterministic summary missing: %v", out)
	}
}

// TestCompactionLLM_ContextCancelled makes sure the turn context reaches the
// generator and a cancelled turn degrades instead of blocking/retrying.
func TestCompactionLLM_ContextCancelled(t *testing.T) {
	cfg := llmTestConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sawCtxErr := false
	opts := &CompactionOptions{
		SummaryGenerator: testGen,
		LLMSummaryGenerator: func(ctx context.Context, _ string, _ []types.Message) (string, error) {
			sawCtxErr = ctx.Err() != nil
			return "", ctx.Err()
		},
	}
	var cache compactionSummaryCache

	_, folded := trimHistoryToTokenBudgetCompactionCtx(
		ctx, msgs(30), cfg.MaxBudgetTokens, cfg, opts, "", nil,
		types.NewAgentInput("input"), &cache)
	if !sawCtxErr {
		t.Fatalf("generator did not receive the turn context")
	}
	if !folded {
		t.Fatalf("cancelled LLM call should fall back to the deterministic summary")
	}
}

// TestCompactionLLM_BudgetNeverExceeded sweeps anchors, budgets and history
// lengths: whenever the LLM summary is folded in, the aggregate must respect
// the strict budget contract (same invariant as the deterministic path).
func TestCompactionLLM_BudgetNeverExceeded(t *testing.T) {
	rec := &llmRecorder{reply: func(int) (string, error) {
		return strings.Repeat("m", 80), nil // small but non-trivial summary
	}}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	for _, anchor := range []int{0, 30, 256, 2048} {
		for _, budget := range []int{80, 120, 400, 1000, 10000} {
			for _, n := range []int{1, 3, 10, 30, 100} {
				cfg := types.NewAgentConfig()
				cfg.SystemMessage = "system"
				cfg.MaxBudgetTokens = budget
				cfg.CacheAnchorTokens = anchor
				var cache compactionSummaryCache
				rem := budget - fixedTokens(cfg, nil, "input")
				if rem <= 0 {
					continue
				}
				out, folded := trimHistoryToTokenBudgetCompactionCtx(
					context.Background(), msgs(n), budget, cfg, opts, "", nil,
					types.NewAgentInput("input"), &cache)
				if folded && msgTokensSum(out) > rem {
					t.Fatalf("budget=%d anchor=%d n=%d: folded output %d > rem %d: %v",
						budget, anchor, n, msgTokensSum(out), rem, out)
				}
			}
		}
	}
}

// TestCompactionLLM_ExistingSummaryInvalidatesCache: a changed GetSummary must
// not reuse a summary that was generated against the old one.
func TestCompactionLLM_ExistingSummaryInvalidatesCache(t *testing.T) {
	cfg := llmTestConfig()
	rec := &llmRecorder{}
	opts := &CompactionOptions{LLMSummaryGenerator: rec.gen}
	var cache compactionSummaryCache
	history := msgs(30)

	trimHistoryToTokenBudgetCompactionCtx(context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "old", nil, types.NewAgentInput("input"), &cache)
	trimHistoryToTokenBudgetCompactionCtx(context.Background(), history, cfg.MaxBudgetTokens, cfg, opts, "new", nil, types.NewAgentInput("input"), &cache)
	if rec.calls != 2 {
		t.Fatalf("LLM calls = %d, want 2 (existingSummary change must invalidate)", rec.calls)
	}
	if !strings.Contains(rec.existing[1], "new") {
		t.Fatalf("second call should receive the new existing summary, got %q", rec.existing[1])
	}
}
