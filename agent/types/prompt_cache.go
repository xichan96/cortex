package types

// PromptCacheOptions controls provider prompt caching (Anthropic cache_control
// breakpoints) and cache usage backfill.
//
// B2 (review): Anthropic enforces a hard limit of MaxAnthropicCacheBreakpoints
// (4) cache_control breakpoints per request; exceeding it returns HTTP 400.
// The layout is therefore budget-based:
//
//	system (≤1) + last tool (≤1) + history (≤ HistoryEveryN, capped to 4 total)
//
// HistoryEveryN is a *budget* for the history segment (how many breakpoints the
// history may consume), NOT an "every N messages" interval. When the budget
// would exceed the hard cap, the layout degrades by dropping history
// breakpoints rather than risking a 400.
type PromptCacheOptions struct {
	Enabled          bool // master switch; when false the request body is byte-identical to pre-caching
	SystemBreakpoint bool // 1 breakpoint on the system block
	ToolsBreakpoint  bool // 1 breakpoint on the LAST tool only
	HistoryEveryN    int  // history breakpoint budget (0 = no history breakpoints)
	MinCacheTokens   int  // history segment below this (estimated tokens) gets no history breakpoint
}

// MaxAnthropicCacheBreakpoints is the hard per-request limit enforced by the
// Anthropic Messages API. Clients must never exceed it.
const MaxAnthropicCacheBreakpoints = 4

// DefaultHistoryBreakpointBudget is the default number of breakpoints the
// history segment may consume (keeps total = system 1 + tool 1 + history 2 = 4).
const DefaultHistoryBreakpointBudget = 2

// DefaultPromptCacheOptions returns the default prompt caching behavior.
//
// R2 (review): MinCacheTokens defaults to 4096 because newer models
// (Opus 4.x, Sonnet 4.6+) require a 2048-4096 token minimum prefix before a
// breakpoint is honored; 1024 would silently not cache on those models.
func DefaultPromptCacheOptions() PromptCacheOptions {
	return PromptCacheOptions{
		Enabled:          true,
		SystemBreakpoint: true,
		ToolsBreakpoint:  true,
		HistoryEveryN:    DefaultHistoryBreakpointBudget,
		MinCacheTokens:   4096,
	}
}

// PromptCacheConfigurer is implemented by LLM providers that support prompt
// cache breakpoint injection (e.g. NativeAnthropicProvider). Defined here in
// agent/types (B3) so the engine can assert it without importing agent/llm.
type PromptCacheConfigurer interface {
	SetPromptCacheOptions(PromptCacheOptions)
}
