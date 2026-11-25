package engine

import (
	"context"
	"errors"
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/agent/utils"
	"github.com/xichan96/cortex/pkg/logger"
)

// ==================== Configuration Management Methods ====================

// SetMemory sets the memory system
func (ae *AgentEngine) SetMemory(ctx context.Context, memory types.MemoryProvider) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.memory = memory

	if ae.config != nil && ae.config.MaxHistoryMessages > 0 {
		if provider, ok := memory.(interface{ SetMaxHistoryMessages(int) }); ok {
			provider.SetMaxHistoryMessages(ae.config.MaxHistoryMessages)
		}
	}
}

// GetMemory returns the memory provider
func (ae *AgentEngine) GetMemory() types.MemoryProvider {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	return ae.memory
}

// ErrMemoryNotConfigured is returned by CompactMemory when no memory provider
// is attached to the engine.
var ErrMemoryNotConfigured = errors.New("engine: memory provider is not configured")

// compactReporter is the optional capability a memory provider may implement to
// report details CompressMemory's error return cannot express. Providers that do
// not implement it still compact; the result just carries zero values for the
// fields it cannot report.
type compactReporter interface {
	// LastCompactUsedLLM reports whether the newest summary was produced by the
	// LLM (false when the deterministic fallback was used, or nothing has been
	// compressed yet).
	LastCompactUsedLLM() bool
	// CompactSummary returns the summary currently in effect. It exists apart
	// from GetSummary because some adapters suppress GetSummary to control
	// prompt injection.
	CompactSummary(ctx context.Context) (string, error)
}

// CompactResult describes what one CompactMemory call did.
//
// Counts are best effort: they are read from the provider before and after
// compression. When the provider exposes neither StoredMessageCount nor a
// working GetChatHistory, Kept stays 0 and Compacted cannot be derived.
type CompactResult struct {
	// Compacted is how many messages were folded into the summary
	// (before - after). 0 means either nothing was compacted (message count at
	// or below the retention window) or the counts could not be read.
	Compacted int `json:"compacted"`
	// Kept is the number of messages still stored after compaction. 0 when the
	// provider does not report a count.
	Kept int `json:"kept"`
	// SummaryLen is the length in bytes of the summary in effect after
	// compaction. 0 when the provider exposes no summary.
	SummaryLen int `json:"summary_len"`
	// UsedLLM is true when the provider reports that the newest summary came
	// from the LLM rather than the deterministic fallback. It stays false for
	// providers that cannot report this.
	UsedLLM bool `json:"used_llm"`
}

// SetCompactionObserver installs a callback for automatically triggered
// compressions. Manual CompactMemory does not invoke it (the caller already
// has the result). Pass nil to clear.
func (ae *AgentEngine) SetCompactionObserver(fn func(reason string, result CompactResult)) {
	ae.mu.Lock()
	ae.compactionObserver = fn
	ae.mu.Unlock()
}

// CompactMemory compacts the conversation memory on demand (the manual /compact
// entry point). It runs the same provider compression the engine schedules
// automatically and serializes with it on memoryCompressMu, so an in-flight
// automatic compression completes first and the two never run concurrently.
//
// Unlike the automatic path, CompactMemory does not gate on
// EnableMemoryCompress/threshold: the caller asked for it. The provider still
// decides whether there is anything to compact (typically a no-op when the
// stored message count is at or below KeepRecentCount).
func (ae *AgentEngine) CompactMemory(ctx context.Context) (*CompactResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	ae.mu.RLock()
	mem := ae.memory
	llm := ae.model
	config := ae.config
	ae.mu.RUnlock()

	if mem == nil {
		return nil, ErrMemoryNotConfigured
	}

	ae.memoryCompressMu.Lock()
	defer ae.memoryCompressMu.Unlock()

	before, hadBefore := ae.storedMessageCount(ctx, mem)

	if err := mem.CompressMemory(ctx, llm, compressKeepWindow(config)); err != nil {
		return nil, err
	}

	return ae.buildCompactResult(ctx, mem, before, hadBefore), nil
}

// buildCompactResult reads the post-compression counts/summary. before/hadBefore
// come from the pre-compression storedMessageCount.
func (ae *AgentEngine) buildCompactResult(ctx context.Context, mem types.MemoryProvider, before int, hadBefore bool) *CompactResult {
	after, hadAfter := ae.storedMessageCount(ctx, mem)

	result := &CompactResult{}
	if hadAfter {
		result.Kept = after
	}
	if hadBefore && hadAfter && before > after {
		result.Compacted = before - after
	}

	if reporter, ok := mem.(compactReporter); ok {
		result.UsedLLM = reporter.LastCompactUsedLLM()
		if summary, err := reporter.CompactSummary(ctx); err == nil {
			result.SummaryLen = len(summary)
		}
	} else if reader, ok := mem.(interface {
		GetSummary(context.Context) (string, error)
	}); ok {
		if summary, err := reader.GetSummary(ctx); err == nil {
			result.SummaryLen = len(summary)
		}
	}
	return result
}

// storedMessageCount reads the raw stored message count, preferring
// StoredMessageCount over GetChatHistory: the latter may include an injected
// summary message (Hybrid appends one) and would skew the before/after delta.
func (ae *AgentEngine) storedMessageCount(ctx context.Context, mem types.MemoryProvider) (int, bool) {
	if ctr, ok := mem.(interface {
		StoredMessageCount(context.Context) (int, error)
	}); ok {
		if n, err := ctr.StoredMessageCount(ctx); err == nil {
			return n, true
		}
	}
	if history, err := mem.GetChatHistory(ctx); err == nil {
		return len(history), true
	}
	return 0, false
}

// compressKeepWindow is the retention window passed to CompressMemory. It is
// shared by the automatic and manual paths so they cannot drift.
func compressKeepWindow(config *types.AgentConfig) int {
	keepWindow := 50
	if config == nil {
		return keepWindow
	}
	if config.MemoryCompressThreshold > 0 {
		keepWindow = config.MemoryCompressThreshold
	} else if config.MaxHistoryMessages > 0 {
		keepWindow = config.MaxHistoryMessages
	}
	return keepWindow
}

// SetOutputParser sets the output parser
func (ae *AgentEngine) SetOutputParser(ctx context.Context, parser types.OutputParser) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.outputParser = parser
}

// Configuration setting helper function
func (ae *AgentEngine) setConfigValue(updateFunc func()) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	if ae.config == nil {
		ae.config = types.NewAgentConfig()
	}
	updateFunc()
}

// SetTemperature sets the temperature parameter
func (ae *AgentEngine) SetTemperature(ctx context.Context, temperature float32) {
	ae.setConfigValue(func() {
		ae.config.Temperature = temperature
	})
}

// SetMaxTokens sets the maximum tokens
func (ae *AgentEngine) SetMaxTokens(ctx context.Context, maxTokens int) {
	ae.setConfigValue(func() {
		ae.config.MaxCompletionTokens = maxTokens
		ae.config.MaxTokens = maxTokens
	})
}

// SetTopP sets Top P sampling
func (ae *AgentEngine) SetTopP(ctx context.Context, topP float32) {
	ae.setConfigValue(func() {
		ae.config.TopP = topP
	})
}

// SetFrequencyPenalty sets frequency penalty
func (ae *AgentEngine) SetFrequencyPenalty(ctx context.Context, penalty float32) {
	ae.setConfigValue(func() {
		ae.config.FrequencyPenalty = penalty
	})
}

// SetPresencePenalty sets presence penalty
func (ae *AgentEngine) SetPresencePenalty(ctx context.Context, penalty float32) {
	ae.setConfigValue(func() {
		ae.config.PresencePenalty = penalty
	})
}

// SetStopSequences sets stop sequences
func (ae *AgentEngine) SetStopSequences(ctx context.Context, sequences []string) {
	ae.setConfigValue(func() {
		ae.config.StopSequences = sequences
	})
}

// SetTimeout sets timeout duration
func (ae *AgentEngine) SetTimeout(ctx context.Context, timeout time.Duration) {
	ae.setConfigValue(func() {
		ae.config.Timeout = timeout
	})
}

// SetRetryAttempts sets retry attempts
func (ae *AgentEngine) SetRetryAttempts(ctx context.Context, attempts int) {
	ae.setConfigValue(func() {
		ae.config.RetryAttempts = attempts
	})
}

// SetRetryDelay sets retry delay
func (ae *AgentEngine) SetRetryDelay(ctx context.Context, delay time.Duration) {
	ae.setConfigValue(func() {
		ae.config.RetryDelay = delay
	})
}

// SetEnableToolRetry sets whether to enable tool retry
func (ae *AgentEngine) SetEnableToolRetry(ctx context.Context, enable bool) {
	ae.setConfigValue(func() {
		ae.config.EnableToolRetry = enable
	})
}

// SetConfig sets the complete configuration
func (ae *AgentEngine) SetConfig(ctx context.Context, config *types.AgentConfig) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	if config == nil {
		ae.config = types.NewAgentConfig()
	} else {
		ae.config = config
	}

	ae.propagateConfigLocked()
}

// propagateConfigLocked applies engine-level config to the LLM provider that
// supports it. Must be called with ae.mu held (or before the engine is shared).
//
// R4 (review): dino/factory.go never calls SetConfig, so NewAgentEngine must
// call this too or the dino default path never enables prompt caching.
func (ae *AgentEngine) propagateConfigLocked() {
	// Propagate logger to model if supported
	if provider, ok := ae.model.(interface{ SetLogger(*logger.Logger) }); ok {
		provider.SetLogger(logger.GetLogger())
	}

	// Propagate prompt caching to providers that implement the configurer.
	// We merge only the Enabled flag onto the provider's existing options so
	// sub-field overrides (e.g. dino's PromptCachingConfig) are preserved.
	if pc, ok := ae.model.(types.PromptCacheConfigurer); ok {
		opts := pc.PromptCacheOptions()
		opts.Enabled = ae.config != nil && ae.config.PromptCaching
		pc.SetPromptCacheOptions(opts)
	}
}

// SetRateLimiter sets the rate limiter
func (ae *AgentEngine) SetRateLimiter(ctx context.Context, limiter utils.RateLimiter) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.rateLimiter = limiter
}

// SetToolCallback sets the tool callback for real-time tool execution events
func (ae *AgentEngine) SetToolCallback(ctx context.Context, callback types.ToolCallback) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.toolCallback = callback
}

// SetHooks sets the hooks for lifecycle events
func (ae *AgentEngine) SetHooks(ctx context.Context, h hooks.Hooks) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.hooks = h
}

// SetCompactionOptions wires prefix-preserving compaction (P3.1). The summary
// generator must come from the constructor (dino/factory.go injects
// chatstore.DeterministicCompact as a closure); the engine itself never
// imports dino (review BLOCKER B1). Pass nil to disable.
func (ae *AgentEngine) SetCompactionOptions(opts *CompactionOptions) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.compaction = opts
}

// GetCompactionOptions returns the current compaction options under the read
// lock (nil when disabled). The Enabled/CacheAnchorTokens switches live on
// types.AgentConfig; the SummaryGenerator lives only here. Exported so the
// constructor (dino/factory.go) and tests can verify the wiring.
func (ae *AgentEngine) GetCompactionOptions() *CompactionOptions {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	return ae.compaction
}

// getCompactionOptions returns the current compaction options under the read
// lock (internal, unexported).
func (ae *AgentEngine) getCompactionOptions() *CompactionOptions {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	return ae.compaction
}
