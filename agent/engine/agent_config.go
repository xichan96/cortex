package engine

import (
	"context"
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/utils"
	"github.com/xichan96/cortex/agent/types"
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
		ae.config.MaxTokens = maxTokens
		ae.config.MaxCompletionTokens = maxTokens // Also set MaxCompletionTokens for compatibility
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

	// Propagate logger to model if supported
	if provider, ok := ae.model.(interface{ SetLogger(*logger.Logger) }); ok {
		provider.SetLogger(logger.GetLogger())
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
