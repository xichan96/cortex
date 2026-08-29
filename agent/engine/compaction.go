package engine

import (
	"strings"

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
}

// summaryMarker prefixes the tail summary message so the model can tell the
// compressed block from real history (Codex SUMMARY_PREFIX model).
const summaryMarker = "[CONVERSATION COMPACTED - prior context follows]\n"

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
func trimHistoryToTokenBudgetCompaction(history []types.Message, maxBudgetTokens int, config *types.AgentConfig, compaction *CompactionOptions, existingSummary string, previousRequests []types.ToolCallData, input types.AgentInput) ([]types.Message, bool) {
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
	return trimHistoryThreePhase(history, rem, anchorTokens, compaction, existingSummary)
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
	if start >= len(history) {
		return history[len(history)-1:]
	}
	return history[start:]
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
// The returned bool reports whether a summary message was inserted (the
// existingSummary content was folded into it).
func trimHistoryThreePhase(history []types.Message, rem int, anchorTokens int, compaction *CompactionOptions, existingSummary string) ([]types.Message, bool) {
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
