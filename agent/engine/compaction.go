package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

// CompactionOptions carries the prefix-preserving compaction behavior
// (P3.1 · prompt caching Step 4). The engine only reads the fields; the
// summary generator is wired at construction time so agent/engine never
// imports dino (review BLOCKER B1).
//
// SummaryGenerator is the P3.4 hook: replace deterministic compaction with an
// LLM summary. The closure is injected by the constructor (dino/factory
// passes chatstore.DeterministicCompact). nil means "drop the middle and keep
// head+tail" — the budget-safe fallback (B1).
//
// The Enabled / CacheAnchorTokens switches live on types.AgentConfig (REC-4c:
// fields go through config, the function goes through injection); the engine
// gates the three-phase path on config.CompactionPrefix and reads the anchor
// budget from config.CacheAnchorTokens.
type CompactionOptions struct {
	SummaryGenerator func(existingSummary string, mid []types.Message) string

	// LLMSummaryGenerator is the optional LLM-backed counterpart. When set the
	// three-phase trim prefers it over SummaryGenerator: it receives the turn
	// context and the middle that is about to be replaced. A non-nil error or a
	// blank result degrades to SummaryGenerator, so the strict budget and
	// message-shape contract never depends on the model succeeding.
	//
	// It is injected by the host (dino/factory wraps chatstore.LLMSummaryAdapter)
	// so agent/engine still never imports dino. The engine caches the generated
	// summary together with the history prefix it covers: repeated
	// prepareMessages calls reuse it while the uncovered tail still fits, which
	// keeps the prompt-cache prefix stable and bounds LLM cost to one call per
	// "middle outgrew the tail budget" event.
	LLMSummaryGenerator func(ctx context.Context, existingSummary string, mid []types.Message) (string, error)
}

// compactionSummaryCache remembers the last LLM-generated middle summary and
// the history prefix it covers. The zero value is ready to use. It is held by
// the engine (one engine per session), so summaries never leak across sessions.
type compactionSummaryCache struct {
	mu          sync.Mutex
	summary     string
	existingKey string // hash of the existingSummary folded into summary
	prefixHash  string // hash of history[:coveredEnd]
	coveredEnd  int    // absolute index into history
}

// compactionHash returns a short stable digest for cache keys.
func compactionHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// compactionHistoryHash digests history[:n]. Hashing the whole prefix (instead
// of matching one message) is duplicate-proof: repeated "ok" messages cannot
// make a rewritten history look like a covered prefix.
func compactionHistoryHash(history []types.Message) string {
	h := sha256.New()
	for _, m := range history {
		args, _ := json.Marshal(m.ToolCalls)
		parts, _ := json.Marshal(m.Parts)
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Name))
		h.Write([]byte{0})
		h.Write([]byte(m.ToolCallID))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
		h.Write(args)
		h.Write([]byte{0})
		h.Write(parts)
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// lookup returns the cached summary when it is still valid for this history and
// existing summary. coveredEnd is the absolute index one past the summarized
// history prefix.
func (c *compactionSummaryCache) lookup(history []types.Message, existingSummary string) (summary string, coveredEnd int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.summary == "" || c.coveredEnd <= 0 || c.coveredEnd > len(history) {
		return "", 0, false
	}
	if c.existingKey != compactionHash(existingSummary) {
		return "", 0, false
	}
	if c.prefixHash != compactionHistoryHash(history[:c.coveredEnd]) {
		return "", 0, false
	}
	return c.summary, c.coveredEnd, true
}

// store records an LLM summary that covers history[:coveredEnd].
func (c *compactionSummaryCache) store(existingSummary string, history []types.Message, coveredEnd int, summary string) {
	if coveredEnd <= 0 || coveredEnd > len(history) {
		return
	}
	prefixHash := compactionHistoryHash(history[:coveredEnd])
	c.mu.Lock()
	defer c.mu.Unlock()
	c.summary = summary
	c.coveredEnd = coveredEnd
	c.prefixHash = prefixHash
	c.existingKey = compactionHash(existingSummary)
}

// summaryMarker prefixes the tail summary message so the model can tell the
// compressed block from real history (Codex SUMMARY_PREFIX model).
const summaryMarker = "[CONVERSATION COMPACTED - prior context follows]\n"

const (
	// compactionLLMSummaryReserveTokens is the tail budget held back for the
	// LLM middle summary. It matches the adapter's ~2000-token output cap; on
	// small budgets it shrinks to half the non-head budget. Reserving before
	// generation keeps the tail start (and therefore the summary coverage)
	// independent of the model's exact output size.
	compactionLLMSummaryReserveTokens = 2048
	// compactionMinTailTokens is the smallest non-head budget worth running
	// the LLM path for; below it the deterministic/head+tail fallback is used.
	compactionMinTailTokens = 2
)

// buildTailSummary renders the middle of the history into a single summary
// string. It never imports dino: when no generator is injected the caller
// degrades to "drop mid, keep head+tail" (reporting false), keeping the
// strict budget contract intact.
func buildTailSummary(cfg *CompactionOptions, existingSummary string, mid []types.Message) (string, bool) {
	if cfg == nil || cfg.SummaryGenerator == nil {
		return "", false
	}
	summary := cfg.SummaryGenerator(existingSummary, mid)
	if strings.TrimSpace(summary) == "" {
		return "", false
	}
	return summary, true
}

// summaryMessage builds an assistant-role summary message, prefixing the
// SUMMARY_PREFIX marker so the model can tell the compressed block from real
// history (Codex SUMMARY_PREFIX model). Role assistant (never system): it does
// not enter the system concatenation, so cache segment 1 (system) is not
// rewritten by compaction, and it alternates with the surrounding user
// messages for Anthropic's mergeConsecutiveRoles.
func summaryMessage(content string) types.Message {
	if !strings.HasPrefix(content, "[CONVERSATION COMPACTED") {
		content = summaryMarker + content
	}
	return types.Message{
		Role:    "assistant",
		Content: content,
	}
}

// trimHistoryToTokenBudget keeps today's behavior (legacy tail-only trim).
// With CompactionPrefix enabled, prepareMessages routes through
// trimHistoryToTokenBudgetCompaction instead, which runs the three-phase trim.
func trimHistoryToTokenBudget(history []types.Message, maxBudgetTokens int, config *types.AgentConfig, previousRequests []types.ToolCallData, input types.AgentInput) []types.Message {
	if len(history) == 0 {
		return history
	}
	fixed := 0
	if config != nil && config.SystemMessage != "" {
		fixed += types.RoughTokensForMessage(types.Message{Role: "system", Content: config.SystemMessage})
	}
	for _, m := range types.MessagesFromToolSteps("", previousRequests) {
		fixed += types.RoughTokensForMessage(m)
	}
	fixed += types.RoughTokensForMessage(input.ToMessage("user"))
	rem := maxBudgetTokens - fixed
	if rem <= 0 {
		return history[len(history)-1:]
	}
	return trimHistoryTailOnly(history, rem)
}

// trimHistoryToTokenBudgetCompaction is the three-phase trim. compaction may
// be nil; existingSummary is the GetSummary string (may be ""), folded into
// the tail summary so cumulative summaries survive. The returned bool reports
// whether existingSummary was folded into an inserted summary message.
//
// It is the context-free entry point (no cache, no LLM reuse across calls)
// used by tests and callers that have no turn context. The engine's
// prepareMessages uses trimHistoryToTokenBudgetCompactionCtx instead.
func trimHistoryToTokenBudgetCompaction(history []types.Message, maxBudgetTokens int, config *types.AgentConfig, compaction *CompactionOptions, existingSummary string, previousRequests []types.ToolCallData, input types.AgentInput) ([]types.Message, bool) {
	return trimHistoryToTokenBudgetCompactionCtx(context.Background(), history, maxBudgetTokens, config, compaction, existingSummary, previousRequests, input, nil)
}

// trimHistoryToTokenBudgetCompactionCtx is trimHistoryToTokenBudgetCompaction
// with the turn context and the per-engine summary cache. cache may be nil
// (no reuse; each call that needs an LLM summary asks for a fresh one).
func trimHistoryToTokenBudgetCompactionCtx(ctx context.Context, history []types.Message, maxBudgetTokens int, config *types.AgentConfig, compaction *CompactionOptions, existingSummary string, previousRequests []types.ToolCallData, input types.AgentInput, cache *compactionSummaryCache) ([]types.Message, bool) {
	if len(history) == 0 {
		return history, false
	}
	fixed := 0
	if config != nil && config.SystemMessage != "" {
		fixed += types.RoughTokensForMessage(types.Message{Role: "system", Content: config.SystemMessage})
	}
	for _, m := range types.MessagesFromToolSteps("", previousRequests) {
		fixed += types.RoughTokensForMessage(m)
	}
	fixed += types.RoughTokensForMessage(input.ToMessage("user"))
	rem := maxBudgetTokens - fixed
	if rem <= 0 {
		return history[len(history)-1:], false
	}
	anchorTokens := 0
	if config != nil {
		anchorTokens = config.CacheAnchorTokens
	}
	return trimHistoryThreePhase(ctx, history, rem, anchorTokens, compaction, existingSummary, cache)
}

// trimHistoryTailOnly keeps today's "keep recent tail, drop head" behavior.
func trimHistoryTailOnly(history []types.Message, rem int) []types.Message {
	used := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		c := types.RoughTokensForMessage(history[i])
		if used+c > rem {
			break
		}
		used += c
		start = i
	}
	start = safeTailBoundary(history, start)
	if start >= len(history) {
		return history[len(history)-1:]
	}
	return history[start:]
}

// safeTailBoundary moves a slice boundary past tool results that would
// otherwise become the first message of the kept tail (a tool_result without
// its tool_use is invalid for both Anthropic and OpenAI). It never moves the
// boundary backwards, so the token budget is preserved.
func safeTailBoundary(history []types.Message, start int) int {
	if start <= 0 || start >= len(history) {
		return start
	}
	for start < len(history) && history[start].Role == "tool" {
		start++
	}
	if start >= len(history) {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role != "tool" {
				return i
			}
		}
		return len(history) - 1
	}
	return start
}

// trimHistoryThreePhase keeps headMsgs (a verbatim prefix within the anchor
// budget), replaces midMsgs with one summary assistant message, and keeps
// tailMsgs (a verbatim suffix).
//
// Budget: the summary reserves its own budget from the tail, so the output
// head + summary + tail ≤ rem holds by construction:
//   - head ≤ anchorTokens (clamped to rem);
//   - the summary is generated for the first middle, then the tail is
//     re-scanned with budget rem - headTokens - tokens(summary), so
//     head + tail + summary ≤ rem;
//   - if head + tail alone exceed rem (oversized head or the legacy
//     "keep one recent message even over budget" degenerate), the function
//     falls back to trimHistoryTailOnly — never worse than today's behavior.
//
// With LLMSummaryGenerator set the middle is summarized by the model instead,
// in a single generation (the deterministic path's second generation would
// double LLM cost), and a validated summary is reused across calls from the
// per-engine cache while the uncovered tail still fits.
//
// The returned bool reports whether a summary message was inserted (the
// existingSummary content was folded into it).
func trimHistoryThreePhase(ctx context.Context, history []types.Message, rem int, anchorTokens int, compaction *CompactionOptions, existingSummary string, cache *compactionSummaryCache) ([]types.Message, bool) {
	if anchorTokens < 0 {
		anchorTokens = 0
	}
	anchorTokens = min(anchorTokens, rem)

	// Head cache anchor: verbatim prefix of history, at least one message.
	// Keeping history[0] (usually the initial user request) also guarantees
	// the summary assistant message is never messages[0] — Anthropic requires
	// the first message to be user (review O1).
	anchorIdx := 1
	headTokens := types.RoughTokensForMessage(history[0])
	for anchorIdx < len(history) && headTokens+types.RoughTokensForMessage(history[anchorIdx]) <= anchorTokens {
		headTokens += types.RoughTokensForMessage(history[anchorIdx])
		anchorIdx++
	}
	if anchorIdx >= len(history) {
		// Head covers the whole history (≤ anchorTokens ≤ rem): nothing to
		// compress.
		return history, false
	}
	head := history[:anchorIdx]

	// Tail with the full remaining budget (no summary reserved yet).
	tailStart, _ := tailScan(history, anchorIdx, rem-headTokens)
	tailStart = safeTailBoundary(history, tailStart)
	tail := history[tailStart:]

	// Base candidate: head + tail (no summary). Aggregate-measured so the
	// oversized-head / oversized-last-message degenerate falls back to the
	// legacy trim instead of growing past rem.
	base := headPlusTail(head, tail)
	if msgSumTokens(base) > rem {
		return trimHistoryTailOnly(history, rem), false
	}
	if tailStart <= anchorIdx {
		// Head and tail together cover the history: nothing to compress.
		return base, false
	}

	// LLM summary path: one model call replaces the deterministic two-pass
	// generation; on failure/empty output the deterministic path below still
	// runs (or the caller degrades to head+tail).
	if compaction != nil && compaction.LLMSummaryGenerator != nil {
		if out, ok := trimHistoryPhaseLLM(ctx, history, anchorIdx, headTokens, rem, compaction, existingSummary, cache); ok {
			return out, true
		}
	}

	// Summary replaces the middle and reserves its budget from the tail.
	summaryText, ok := buildTailSummary(compaction, existingSummary, history[anchorIdx:tailStart])
	if !ok {
		return base, false
	}
	sTokens := types.RoughTokensForMessage(summaryMessage(summaryText))

	tailBudget := rem - headTokens - sTokens
	if tailBudget < 0 {
		// Summary + head alone exceed rem: drop the summary, keep head+tail.
		return base, false
	}
	tailStart2, _ := tailScan(history, anchorIdx, tailBudget)
	tailStart2 = safeTailBoundary(history, tailStart2)
	if tailStart2 <= anchorIdx {
		// No room for both the summary and any tail: prefer head + tail.
		return base, false
	}
	// Re-generate the summary for the (smaller) final middle and verify.
	summaryText2, ok2 := buildTailSummary(compaction, existingSummary, history[anchorIdx:tailStart2])
	if !ok2 {
		return headPlusTail(head, history[tailStart2:]), false
	}
	final := headPlusTail(head, append([]types.Message{summaryMessage(summaryText2)}, history[tailStart2:]...))
	if msgSumTokens(final) > rem {
		// Verification failed: drop the summary, keep head + tail.
		return headPlusTail(head, history[tailStart2:]), false
	}
	return final, true
}

// trimHistoryPhaseLLM performs the LLM variant of the three-phase trim:
//
//  1. If the cache holds a summary that still covers the head anchor and the
//     uncovered messages fit in the tail, emit head + cached summary + tail
//     verbatim. This is the prompt-cache-stable steady state: the summary does
//     not change between calls, so only the appended tail is new.
//  2. Otherwise ask the model for a summary. When a valid cached summary
//     exists, only the uncovered delta is summarized and the cached summary is
//     folded in as existingSummary (cumulative, no re-sending). The result is
//     cached with the history prefix it covers.
//
// A failure at any point returns ok=false so trimHistoryThreePhase can fall
// back to the deterministic generator (or head+tail).
//
// Coverage is chosen before generation: up to compactionLLMSummaryReserveTokens
// is held back for the summary (half the non-head budget on small budgets).
// Reserving up-front avoids a circular "summary size determines the tail
// start, which determines what to summarize" loop and one wasted extra call.
func trimHistoryPhaseLLM(ctx context.Context, history []types.Message, anchorIdx, headTokens, rem int, compaction *CompactionOptions, existingSummary string, cache *compactionSummaryCache) ([]types.Message, bool) {
	available := rem - headTokens
	if available <= compactionMinTailTokens {
		return nil, false
	}
	reserve := min(compactionLLMSummaryReserveTokens, available/2)
	if reserve < 1 {
		return nil, false
	}
	coverage, _ := tailScan(history, anchorIdx, available-reserve)
	coverage = safeTailBoundary(history, coverage)
	if coverage <= anchorIdx {
		return nil, false
	}
	head := history[:anchorIdx]

	// Reuse a valid cached summary (steady state, no LLM call); otherwise
	// summarize only the uncovered delta, folding the cached summary in.
	start := anchorIdx
	existing := existingSummary
	if cache != nil {
		if summary, coveredEnd, ok := cache.lookup(history, existingSummary); ok && coveredEnd >= anchorIdx {
			if out, ok := llmPhaseOutput(head, history, coveredEnd, rem, headTokens, summary); ok {
				return out, true
			}
			if coveredEnd >= coverage {
				// The cached summary covers the whole window but does not fit
				// the reserved slot: let the deterministic path handle it.
				return nil, false
			}
			start = coveredEnd
			existing = summary
		}
	}
	mid := history[start:coverage]
	if len(mid) == 0 {
		return nil, false
	}
	summary, err := compaction.LLMSummaryGenerator(ctx, existing, mid)
	if err != nil || strings.TrimSpace(summary) == "" {
		return nil, false
	}
	summary = strings.TrimSpace(summary)
	if cache != nil {
		// The cache keys on the caller's existingSummary (not the folded
		// cumulative text) so a changed GetSummary invalidates it.
		cache.store(existingSummary, history, coverage, summary)
	}
	return llmPhaseOutput(head, history, coverage, rem, headTokens, summary)
}

// llmPhaseOutput composes head + summary + tail, shrinking the tail so the
// aggregate stays within rem. coveredEnd is the index one past the summarized
// prefix; the tail must start at or before it, otherwise the messages in
// between would be dropped without being summarized (returns ok=false).
func llmPhaseOutput(head, history []types.Message, coveredEnd, rem, headTokens int, summary string) ([]types.Message, bool) {
	sTokens := types.RoughTokensForMessage(summaryMessage(summary))
	tailBudget := rem - headTokens - sTokens
	if tailBudget < 0 {
		return nil, false
	}
	// Scan from the anchor (not from coveredEnd): the summary may be smaller
	// than the reserved slot, so the tail can keep more than the coverage
	// boundary. The only forbidden outcome is a tail that starts *after*
	// coveredEnd — that would drop unsummarized messages.
	tailStart, _ := tailScan(history, len(head), tailBudget)
	tailStart = safeTailBoundary(history, tailStart)
	if tailStart > coveredEnd || tailStart >= len(history) {
		return nil, false
	}
	final := headPlusTail(head, append([]types.Message{summaryMessage(summary)}, history[tailStart:]...))
	if msgSumTokens(final) > rem {
		return nil, false
	}
	return final, true
}

// headPlusTail concatenates two message slices.
func headPlusTail(a, b []types.Message) []types.Message {
	out := make([]types.Message, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

// msgSumTokens totals RoughTokensForMessage across a message slice.
func msgSumTokens(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += types.RoughTokensForMessage(m)
	}
	return n
}

// tailScan returns (start, usedTokens) for the verbatim suffix of history from
// index len-1 down to minIdx that fits in budget. At least one message is kept
// (the last one) even if it alone overflows — legacy semantics.
func tailScan(history []types.Message, minIdx int, budget int) (int, int) {
	start := len(history)
	used := 0
	for i := len(history) - 1; i >= minIdx; i-- {
		c := types.RoughTokensForMessage(history[i])
		if used+c > budget {
			if start == len(history) {
				// Nothing fit: keep the last message anyway (legacy).
				return len(history) - 1, types.RoughTokensForMessage(history[len(history)-1])
			}
			break
		}
		used += c
		start = i
	}
	if start >= len(history) {
		// Nothing fit: keep the last message anyway (legacy).
		return len(history) - 1, types.RoughTokensForMessage(history[len(history)-1])
	}
	return start, used
}
