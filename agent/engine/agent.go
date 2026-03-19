package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/agent/utils"
	"github.com/xichan96/cortex/pkg/logger"
)

// Agent agent engine interface
type Agent interface {
	// Configuration setting methods
	SetMemory(ctx context.Context, memory types.MemoryProvider)
	SetOutputParser(ctx context.Context, parser types.OutputParser)
	SetTemperature(ctx context.Context, temperature float32)
	SetMaxTokens(ctx context.Context, maxTokens int)
	SetTopP(ctx context.Context, topP float32)
	SetFrequencyPenalty(ctx context.Context, penalty float32)
	SetPresencePenalty(ctx context.Context, penalty float32)
	SetStopSequences(ctx context.Context, sequences []string)
	SetTimeout(ctx context.Context, timeout time.Duration)
	SetRetryAttempts(ctx context.Context, attempts int)
	SetRetryDelay(ctx context.Context, delay time.Duration)
	SetEnableToolRetry(ctx context.Context, enable bool)
	SetConfig(ctx context.Context, config *types.AgentConfig)
	SetRateLimiter(ctx context.Context, limiter utils.RateLimiter)
	SetToolCallback(ctx context.Context, callback types.ToolCallback)
	SetHooks(ctx context.Context, h hooks.Hooks)

	// Tool management methods
	AddTool(ctx context.Context, tool types.Tool)
	AddTools(ctx context.Context, tools []types.Tool)

	// Execution methods
	Execute(ctx context.Context, input types.AgentInput, previousRequests []types.ToolCallData) (*types.AgentResult, error)
	ExecuteStream(ctx context.Context, input types.AgentInput, previousRequests []types.ToolCallData) (<-chan types.StreamResult, error)

	// Lifecycle management
	Stop(ctx context.Context)

	// Usage tracking
	GetTotalUsage() types.Usage
}

// AgentEngine agent engine
// Provides intelligent agent functionality with tool calling, streaming, caching, and memory systems
type AgentEngine struct {
	_ Agent // Ensure AgentEngine implements the Agent interface

	// Core components
	model        types.LLMProvider     // LLM model provider
	tools        []types.Tool          // Available tools list
	toolsMap     map[string]types.Tool // Tool mapping table for quick lookup
	memory       types.MemoryProvider  // Memory system
	outputParser types.OutputParser    // Output parser

	// Configuration and state
	config *types.AgentConfig // Engine configuration

	// Internal state management
	mu        sync.RWMutex       // State mutex lock
	isRunning atomic.Bool        // Running state (atomic for thread safety)
	ctx       context.Context    // Context
	cancel    context.CancelFunc // Cancel function

	// Performance optimization
	toolCache     map[string]*types.ToolCacheEntry // Tool execution result cache
	toolCacheMu   sync.RWMutex                     // Cache read-write lock
	toolCacheSize int                              // Cache size limit
	toolCacheHead *types.ToolCacheEntry            // LRU list head (most recently used)
	toolCacheTail *types.ToolCacheEntry            // LRU list tail (least recently used)

	// Rate limiting
	rateLimiter utils.RateLimiter

	// Usage tracking
	totalUsage types.Usage // Total token usage

	// Tool callback for real-time events
	toolCallback types.ToolCallback

	// Result sender for streaming tool events
	resultSender func(types.StreamResult)

	// Hooks for lifecycle events
	hooks hooks.Hooks
}

// ResultSender is a function type for sending stream results
type ResultSender func(types.StreamResult)

// NewAgentEngine creates a new agent engine
// Parameters:
//   - model: LLM model provider
//   - config: agent configuration (if nil, uses default configuration)
//
// Returns:
//   - initialized AgentEngine instance
func NewAgentEngine(model types.LLMProvider, config *types.AgentConfig) *AgentEngine {
	ctx, cancel := context.WithCancel(context.Background())

	if config == nil {
		config = types.NewAgentConfig()
	}

	ae := &AgentEngine{
		model:         model,
		config:        config,
		tools:         make([]types.Tool, 0),
		toolsMap:      make(map[string]types.Tool),
		toolCache:     make(map[string]*types.ToolCacheEntry),
		toolCacheSize: types.DefaultCacheSize, // Using constant-defined cache size
		ctx:           ctx,
		cancel:        cancel,
		rateLimiter:   utils.NewTokenBucket(10, 10),
	}

	// Propagate logger to model if supported
	if provider, ok := model.(interface{ SetLogger(*logger.Logger) }); ok {
		provider.SetLogger(logger.GetLogger())
	}

	return ae
}

// NewAgent creates a new agent instance (via interface)
func NewAgent(model types.LLMProvider, config *types.AgentConfig) Agent {
	return NewAgentEngine(model, config)
}

// GetTotalUsage gets the total token usage
func (ae *AgentEngine) GetTotalUsage() types.Usage {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	return ae.totalUsage
}

// Stop stops the agent engine
// Safely stops the agent engine and releases resources
func (ae *AgentEngine) Stop(ctx context.Context) {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	if ae.cancel != nil {
		ae.cancel()
	}
	ae.isRunning.Store(false)
}
