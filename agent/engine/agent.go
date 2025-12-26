package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/agent/ratelimit"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

// Agent agent engine interface
type Agent interface {
	// Configuration setting methods
	SetMemory(memory types.MemoryProvider)
	SetOutputParser(parser types.OutputParser)
	SetTemperature(temperature float32)
	SetMaxTokens(maxTokens int)
	SetTopP(topP float32)
	SetFrequencyPenalty(penalty float32)
	SetPresencePenalty(penalty float32)
	SetStopSequences(sequences []string)
	SetTimeout(timeout time.Duration)
	SetRetryAttempts(attempts int)
	SetRetryDelay(delay time.Duration)
	SetEnableToolRetry(enable bool)
	SetConfig(config *types.AgentConfig)
	SetRateLimiter(limiter ratelimit.RateLimiter)

	// Tool management methods
	AddTool(tool types.Tool)
	AddTools(tools []types.Tool)

	// Execution methods
	Execute(input string, previousRequests []types.ToolCallData) (*AgentResult, error)
	ExecuteStream(input string, previousRequests []types.ToolCallData) (<-chan StreamResult, error)

	// Lifecycle management
	Stop()
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
	logger *logger.Logger     // Structured logger

	// Internal state management
	mu        sync.RWMutex       // State mutex lock
	isRunning atomic.Bool        // Running state (atomic for thread safety)
	ctx       context.Context    // Context
	cancel    context.CancelFunc // Cancel function

	// Performance optimization
	toolCache     map[string]*toolCacheEntry // Tool execution result cache
	toolCacheMu   sync.RWMutex               // Cache read-write lock
	toolCacheSize int                        // Cache size limit
	toolCacheHead *toolCacheEntry            // LRU list head (most recently used)
	toolCacheTail *toolCacheEntry            // LRU list tail (least recently used)

	// Rate limiting
	rateLimiter ratelimit.RateLimiter // Rate limiter for request throttling
}

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

	return &AgentEngine{
		model:         model,
		config:        config,
		tools:         make([]types.Tool, 0),
		toolsMap:      make(map[string]types.Tool),
		toolCache:     make(map[string]*toolCacheEntry),
		toolCacheSize: DefaultCacheSize, // Using constant-defined cache size
		logger:        logger.NewLogger(),
		ctx:           ctx,
		cancel:        cancel,
		rateLimiter:   ratelimit.NewTokenBucketLimiter(10, 10), // 10 req/s default
	}
}

// NewAgent creates a new agent instance (via interface)
func NewAgent(model types.LLMProvider, config *types.AgentConfig) Agent {
	return NewAgentEngine(model, config)
}

// ==================== Configuration Management Methods ====================

// SetMemory sets the memory system
func (ae *AgentEngine) SetMemory(memory types.MemoryProvider) {
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
func (ae *AgentEngine) SetOutputParser(parser types.OutputParser) {
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
func (ae *AgentEngine) SetTemperature(temperature float32) {
	ae.setConfigValue(func() {
		ae.config.Temperature = temperature
	})
}

// SetMaxTokens sets the maximum tokens
func (ae *AgentEngine) SetMaxTokens(maxTokens int) {
	ae.setConfigValue(func() {
		ae.config.MaxTokens = maxTokens
	})
}

// SetTopP sets Top P sampling
func (ae *AgentEngine) SetTopP(topP float32) {
	ae.setConfigValue(func() {
		ae.config.TopP = topP
	})
}

// SetFrequencyPenalty sets frequency penalty
func (ae *AgentEngine) SetFrequencyPenalty(penalty float32) {
	ae.setConfigValue(func() {
		ae.config.FrequencyPenalty = penalty
	})
}

// SetPresencePenalty sets presence penalty
func (ae *AgentEngine) SetPresencePenalty(penalty float32) {
	ae.setConfigValue(func() {
		ae.config.PresencePenalty = penalty
	})
}

// SetStopSequences sets stop sequences
func (ae *AgentEngine) SetStopSequences(sequences []string) {
	ae.setConfigValue(func() {
		ae.config.StopSequences = sequences
	})
}

// SetTimeout sets timeout duration
func (ae *AgentEngine) SetTimeout(timeout time.Duration) {
	ae.setConfigValue(func() {
		ae.config.Timeout = timeout
	})
}

// SetRetryAttempts sets retry attempts
func (ae *AgentEngine) SetRetryAttempts(attempts int) {
	ae.setConfigValue(func() {
		ae.config.RetryAttempts = attempts
	})
}

// SetRetryDelay sets retry delay
func (ae *AgentEngine) SetRetryDelay(delay time.Duration) {
	ae.setConfigValue(func() {
		ae.config.RetryDelay = delay
	})
}

// SetEnableToolRetry sets whether to enable tool retry
func (ae *AgentEngine) SetEnableToolRetry(enable bool) {
	ae.setConfigValue(func() {
		ae.config.EnableToolRetry = enable
	})
}

// SetConfig sets the complete configuration
func (ae *AgentEngine) SetConfig(config *types.AgentConfig) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	if config == nil {
		ae.config = types.NewAgentConfig()
	} else {
		ae.config = config
	}
}

// SetRateLimiter sets the rate limiter
func (ae *AgentEngine) SetRateLimiter(limiter ratelimit.RateLimiter) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.rateLimiter = limiter
}

// AddTool adds a tool
func (ae *AgentEngine) AddTool(tool types.Tool) {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	toolName := tool.Name()
	ae.tools = append(ae.tools, tool)
	ae.toolsMap[toolName] = tool
}

// ==================== Tool Management Methods ====================

// AddTools adds multiple tools
func (ae *AgentEngine) AddTools(tools []types.Tool) {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	for _, tool := range tools {
		toolName := tool.Name()
		ae.tools = append(ae.tools, tool)
		ae.toolsMap[toolName] = tool
	}
}

// ==================== Core Execution Methods ====================

// Execute executes the agent task (supports multi-round iteration)
// Processes user input with tool calling and multi-round iteration, returning the complete execution result
// Parameters:
//   - input: user input text
//   - previousRequests: previous tool call request history
//
// Returns:
//   - execution result containing output, tool calls, and intermediate steps
//   - error information
func (ae *AgentEngine) Execute(input string, previousRequests []types.ToolCallData) (*AgentResult, error) {
	if !ae.isRunning.CompareAndSwap(false, true) {
		return nil, errors.EC_AGENT_BUSY
	}

	defer ae.isRunning.Store(false)

	// Add execution tracking
	startTime := time.Now()
	ae.logger.LogExecution("Execute", 0, "Starting agent execution",
		slog.String("input", truncateString(input, 100)),
		slog.Int("previousRequests", len(previousRequests)))

	ae.mu.RLock()
	limiter := ae.rateLimiter
	ctx := ae.ctx
	ae.mu.RUnlock()

	if limiter != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := limiter.Wait(ctx); err != nil {
			ae.logger.LogError("Execute", err, slog.String("phase", "rate_limit"))
			return nil, errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err)
		}
	}

	// Pre-allocate slice capacity to reduce memory reallocations
	messages, err := ae.prepareMessages(input, previousRequests)
	if err != nil {
		ae.logger.LogError("Execute", err, slog.String("phase", "prepare_messages"))
		return nil, errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, errors.EC_PREPARE_MESSAGES_FAILED.Message).Wrap(err)
	}

	var finalResult *AgentResult
	iteration := 0
	ae.mu.RLock()
	maxIterations := 10
	if ae.config != nil {
		maxIterations = ae.config.MaxIterations
	}
	ae.mu.RUnlock()

	// Initialize finalResult to prevent nil pointer panic
	finalResult = &AgentResult{Output: ""}

	// Iterate until no tool calls or maximum iterations reached
	for iteration < maxIterations {
		ae.logger.LogExecution("Execute", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

		// Execute single iteration
		result, continueIterating, err := ae.executeIteration(messages, iteration)
		if err != nil {
			ae.logger.LogError("Execute", err, slog.Int("iteration", iteration+1))
			return nil, errors.NewError(errors.EC_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err)
		}

		// Save final result
		finalResult = result

		// If no tool calls or continuation not needed, end
		if !continueIterating || len(result.ToolCalls) == 0 {
			ae.logger.LogExecution("Execute", iteration, "Execution completed, no more tool calls")
			break
		}

		// Prepare next round messages
		messages = ae.buildNextMessages(messages, result)
		iteration++

		// Avoid too fast execution - only delay if there are more iterations
		if iteration < maxIterations {
			ae.logger.LogExecution("Execute", iteration, "Preparing next iteration")
			time.Sleep(IterationDelay)
		} else {
			ae.logger.LogExecution("Execute", iteration, "Reached maximum iterations")
		}
	}

	if iteration >= maxIterations {
		ae.logger.LogExecution("Execute", iteration, fmt.Sprintf("Reached maximum iteration limit: %d", maxIterations))
	}

	executionTime := time.Since(startTime)
	outputLength := 0
	if finalResult != nil {
		outputLength = len(finalResult.Output)
	}
	ae.logger.LogExecution("Execute", 0, "Agent execution completed successfully",
		slog.Duration("total_duration", executionTime),
		slog.Int("total_iterations", iteration+1),
		slog.Int("output_length", outputLength))

	// Save to memory system
	if ae.memory != nil && finalResult != nil {
		inputMap := map[string]interface{}{"input": input}
		outputMap := map[string]interface{}{"output": finalResult.Output}
		if err := ae.memory.SaveContext(inputMap, outputMap); err != nil {
			ae.logger.LogError("Execute", err, slog.String("phase", "save_context"))
			// Do not interrupt execution as main flow is complete
		} else {
			// Check if memory compression is needed
			ae.mu.RLock()
			enableCompress := false
			compressThreshold := 0
			if ae.config != nil {
				enableCompress = ae.config.EnableMemoryCompress
				compressThreshold = ae.config.MemoryCompressThreshold
			}
			ae.mu.RUnlock()

			if enableCompress && compressThreshold > 0 {
				history, err := ae.memory.GetChatHistory()
				if err == nil && len(history) > compressThreshold {
					ae.mu.RLock()
					llm := ae.model
					ae.mu.RUnlock()
					if llm != nil {
						if err := ae.memory.CompressMemory(llm, compressThreshold); err != nil {
							ae.logger.LogError("Execute", err, slog.String("phase", "compress_memory"))
						} else {
							ae.logger.Info("Memory compressed successfully",
								slog.Int("original_count", len(history)),
								slog.Int("threshold", compressThreshold))
						}
					}
				}
			}
		}
	}

	return finalResult, nil
}

// ExecuteStream executes the agent task with streaming (supports multi-round iteration)
// Processes user input with real-time streaming output and multi-round tool calling
// Parameters:
//   - input: user input text
//   - previousRequests: previous tool call request history
//
// Returns:
//   - streaming result channel for real-time content delivery during execution
//   - error information (only during initialization)
func (ae *AgentEngine) ExecuteStream(input string, previousRequests []types.ToolCallData) (<-chan StreamResult, error) {
	if !ae.isRunning.CompareAndSwap(false, true) {
		return nil, errors.EC_AGENT_BUSY
	}

	resultChan := make(chan StreamResult, DefaultChannelBuffer)

	go func() {
		defer close(resultChan)
		defer ae.isRunning.Store(false)

		startTime := time.Now()
		ae.logger.LogExecution("ExecuteStream", 0, "Starting stream execution", slog.String("input", truncateString(input, 100)), slog.Int("previousRequests", len(previousRequests)))

		ae.mu.RLock()
		limiter := ae.rateLimiter
		ctx := ae.ctx
		ae.mu.RUnlock()

		if limiter != nil {
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := limiter.Wait(ctx); err != nil {
				ae.logger.LogError("ExecuteStream", err, slog.String("phase", "rate_limit"))
				resultChan <- StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err),
				}
				return
			}
		}

		defer func() {
			if r := recover(); r != nil {
				ae.logger.LogError("ExecuteStream", fmt.Errorf("panic recovered: %v", r))
				resultChan <- StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_STREAM_PANIC.Code, "panic in stream execution").Wrap(fmt.Errorf("%v", r)),
				}
			}
		}()

		// Prepare initial messages
		messages, err := ae.prepareMessages(input, previousRequests)
		if err != nil {
			ae.logger.LogError("ExecuteStream", err, slog.String("phase", "prepare_messages"))
			resultChan <- StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, "failed to prepare messages").Wrap(err),
			}
			return
		}

		// Stream iterative execution
		ae.executeStreamWithIterations(messages, resultChan)

		ae.logger.LogExecution("ExecuteStream", 0, "Stream execution completed", slog.Duration("total_duration", time.Since(startTime)))
	}()

	return resultChan, nil
}

// prepareMessages prepares messages
// Builds a complete message list including system messages, chat history, tool call context, and user input
// Parameters:
//   - input: user input
//   - previousRequests: previous tool call requests
//
// Returns:
//   - built message list
//   - error information
func (ae *AgentEngine) prepareMessages(input string, previousRequests []types.ToolCallData) ([]types.Message, error) {
	var history []types.Message
	var historyErr error
	if ae.memory != nil {
		history, historyErr = ae.memory.GetChatHistory()
		if historyErr != nil {
			return nil, errors.NewError(errors.EC_MEMORY_HISTORY_FAILED.Code, errors.EC_MEMORY_HISTORY_FAILED.Message).Wrap(historyErr)
		}
	}

	ae.mu.RLock()
	config := ae.config
	ae.mu.RUnlock()

	estimatedSize := 1 +
		len(history) +
		len(previousRequests)
	if config != nil && config.SystemMessage != "" {
		estimatedSize++
	}

	messages := make([]types.Message, 0, estimatedSize)

	if config != nil && config.SystemMessage != "" {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: config.SystemMessage,
		})
	}

	if len(history) > 0 {
		if config != nil && config.MaxHistoryMessages > 0 && len(history) > config.MaxHistoryMessages {
			history = history[len(history)-config.MaxHistoryMessages:]
		}
		messages = append(messages, history...)
	}

	// Add tool call context if previous requests exist
	if len(previousRequests) > 0 {
		context := ae.buildContextFromPreviousRequests(previousRequests)
		messages = append(messages, types.Message{
			Role:    "system",
			Content: context,
		})
	}

	// Add user input
	messages = append(messages, types.Message{
		Role:    "user",
		Content: input,
	})

	return messages, nil
}

// buildContextFromPreviousRequests builds context from previous requests
func (ae *AgentEngine) buildContextFromPreviousRequests(requests []types.ToolCallData) string {
	var builder strings.Builder
	builder.Grow(256 * len(requests))
	builder.WriteString("Previous tool calls:\n")
	for _, req := range requests {
		builder.WriteString(fmt.Sprintf("Tool: %s, Input: %v, Result: %s\n",
			req.Action.Tool, req.Action.ToolInput, req.Observation))
	}
	return builder.String()
}

// executeIteration executes a single iteration
// Processes one round of LLM calling and tool execution, supporting caching and error handling
// Parameters:
//   - messages: current round messages
//   - iteration: current iteration index
//
// Returns:
//   - execution result
//   - whether to continue iteration
//   - error information
func (ae *AgentEngine) executeIteration(messages []types.Message, iteration int) (*AgentResult, bool, error) {
	ae.mu.RLock()
	maxIterations := 10
	timeout := time.Duration(0)
	toolExecutionTimeout := time.Duration(0)
	if ae.config != nil {
		maxIterations = ae.config.MaxIterations
		timeout = ae.config.Timeout
		toolExecutionTimeout = ae.config.ToolExecutionTimeout
	}
	tools := ae.tools
	ctx := ae.ctx
	ae.mu.RUnlock()
	startTime := time.Now()
	ae.logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

	// Create context with timeout if configured
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if ae.model == nil {
		return nil, false, errors.NewError(errors.EC_LLM_CALL_FAILED.Code, "LLM model provider is nil")
	}

	response, err := ae.model.ChatWithTools(messages, tools)
	if err != nil {
		ae.logger.LogError("executeIteration", err, slog.Int("iteration", iteration))
		return nil, false, errors.NewError(errors.EC_CHAT_FAILED.Code, "failed to chat with tools").Wrap(err)
	}

	result := &AgentResult{
		Output: response.Content,
	}

	// Handle tool calls
	if len(response.ToolCalls) > 0 {
		ae.logger.Info("LLM requested tool calls",
			slog.Int("tool_count", len(response.ToolCalls)),
			slog.Int("iteration", iteration+1))

		if iteration+1 >= maxIterations {
			ae.logger.Info("Reached maximum iterations, skipping tool execution",
				slog.Int("iteration", iteration+1),
				slog.Int("max_iterations", maxIterations))
			return result, false, nil
		}

		// Sort tool calls by priority and dependencies
		sortedToolCalls, err := ae.sortToolCallsByDependencies(response.ToolCalls)
		if err != nil {
			ae.logger.LogError("executeIteration", err, slog.String("phase", "sort_tool_calls"))
			// Continue with original order if sorting fails
			sortedToolCalls = response.ToolCalls
		}

		toolCalls := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
		intermediateSteps := make([]types.ToolCallData, 0, len(sortedToolCalls))

		for _, toolCall := range sortedToolCalls {
			ae.logger.Info("Executing tool",
				slog.String("tool_name", toolCall.Function.Name),
				slog.Int("iteration", iteration+1))

			ae.mu.RLock()
			tool, exists := ae.toolsMap[toolCall.Function.Name]
			ae.mu.RUnlock()
			if !exists {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", toolCall.Function.Name)
				ae.logger.Info("Tool not found",
					slog.String("tool_name", toolCall.Function.Name),
					slog.Int("iteration", iteration+1))
				intermediateSteps = append(intermediateSteps, types.ToolCallData{
					Action: types.ToolActionStep{
						Tool:       toolCall.Function.Name,
						ToolInput:  toolCall.Function.Arguments,
						ToolCallID: toolCall.ID,
						Type:       toolCall.Type,
					},
					Observation: errMsg,
				})
				continue
			}

			// Check cache
			toolStartTime := time.Now()
			toolResult, err, cached := ae.getCachedToolResult(toolCall.Function.Name, toolCall.Function.Arguments)
			if cached {
				ae.logger.LogToolExecution(toolCall.Function.Name, true, 0, slog.Bool("cached", true))
				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed (cached error): %v", toolCall.Function.Name, err)
					ae.logger.LogToolExecution(toolCall.Function.Name, false, 0,
						slog.String("error", err.Error()),
						slog.Bool("cached", true))
					intermediateSteps = append(intermediateSteps, types.ToolCallData{
						Action: types.ToolActionStep{
							Tool:       toolCall.Function.Name,
							ToolInput:  toolCall.Function.Arguments,
							ToolCallID: toolCall.ID,
							Type:       toolCall.Type,
						},
						Observation: errMsg,
					})
					continue
				}
			} else {
				// Execute tool with timeout
				toolResult, err = ae.executeToolWithTimeout(tool, toolCall.Function.Arguments, toolExecutionTimeout)
				duration := time.Since(toolStartTime)

				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed: %v", toolCall.Function.Name, err)
					ae.logger.LogToolExecution(toolCall.Function.Name, false, duration,
						slog.String("error", err.Error()),
						slog.String("tool_input", fmt.Sprintf("%v", toolCall.Function.Arguments)))
					intermediateSteps = append(intermediateSteps, types.ToolCallData{
						Action: types.ToolActionStep{
							Tool:       toolCall.Function.Name,
							ToolInput:  toolCall.Function.Arguments,
							ToolCallID: toolCall.ID,
							Type:       toolCall.Type,
						},
						Observation: errMsg,
					})
					continue
				}

				// Cache tool result
				ae.setCachedToolResult(toolCall.Function.Name, toolCall.Function.Arguments, toolResult, err)
				ae.logger.LogToolExecution(toolCall.Function.Name, true, duration, slog.Bool("cached", false))
			}

			ae.logger.Info("Tool executed successfully",
				slog.String("tool_name", toolCall.Function.Name),
				slog.Int("iteration", iteration+1))

			toolCalls = append(toolCalls, types.ToolCallRequest{
				Tool:       toolCall.Function.Name,
				ToolInput:  toolCall.Function.Arguments,
				ToolCallID: toolCall.ID,
				Type:       toolCall.Type,
			})

			// Format observation from tool result
			truncationLength := ae.getToolTruncationLength(toolCall.Function.Name)
			observation := truncateString(formatToolResult(toolResult), truncationLength)

			intermediateSteps = append(intermediateSteps, types.ToolCallData{
				Action: types.ToolActionStep{
					Tool:       toolCall.Function.Name,
					ToolInput:  toolCall.Function.Arguments,
					ToolCallID: toolCall.ID,
					Type:       toolCall.Type,
				},
				Observation: observation,
			})
		}

		result.ToolCalls = toolCalls
		result.IntermediateSteps = intermediateSteps

		// Log iteration completion information
		ae.logger.LogExecution("executeIteration", iteration,
			fmt.Sprintf("Iteration %d completed with %d tool calls", iteration+1, len(toolCalls)),
			slog.Int("tool_calls", len(toolCalls)),
			slog.Duration("duration", time.Since(startTime)))

		// If there are tool calls, usually need to continue iteration
		return result, len(toolCalls) > 0, nil
	}

	ae.logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Iteration %d completed with no tool calls", iteration+1))
	return result, false, nil
}

// ==================== Message Building Methods ====================

// buildNextMessages builds messages for the next round
func (ae *AgentEngine) buildNextMessages(previousMessages []types.Message, result *AgentResult) []types.Message {
	// Keep system messages, user's original question, and assistant's previous response
	// Pre-allocate slice capacity: system messages + user message + assistant response + tool results
	messages := make([]types.Message, 0, 4)

	// Keep system messages (if any)
	for _, msg := range previousMessages {
		if msg.Role == "system" {
			messages = append(messages, msg)
		}
	}

	// Keep user's original question (last user/human message)
	for i := len(previousMessages) - 1; i >= 0; i-- {
		if previousMessages[i].Role == "user" || previousMessages[i].Role == "human" {
			messages = append(messages, previousMessages[i])
			break
		}
	}

	// Keep assistant's previous response if it has content
	// This preserves context between iterations
	if result != nil && result.Output != "" {
		// Convert ToolCallRequest to ToolCall for message format
		toolCalls := make([]types.ToolCall, 0, len(result.ToolCalls))
		for _, tc := range result.ToolCalls {
			toolCalls = append(toolCalls, types.ToolCall{
				ID:   tc.ToolCallID,
				Type: tc.Type,
				Function: types.ToolFunction{
					Name:      tc.Tool,
					Arguments: tc.ToolInput,
				},
			})
		}
		messages = append(messages, types.Message{
			Role:      "assistant",
			Content:   result.Output,
			ToolCalls: toolCalls,
		})
	}

	// Build summary of tool execution results
	var toolResults strings.Builder
	if result != nil && len(result.IntermediateSteps) > 0 {
		toolResults.WriteString("Based on previous tool execution results:\n")
		for _, step := range result.IntermediateSteps {
			toolResults.WriteString(fmt.Sprintf("- Tool %s returned: %s\n", step.Action.Tool, step.Observation))
		}
		toolResults.WriteString("\nPlease continue analysis or complete the task based on these results.")
	}

	// Add tool call results to messages
	if toolResults.Len() > 0 {
		toolResultMessage := types.Message{
			Role:    "user",
			Content: toolResults.String(),
		}
		messages = append(messages, toolResultMessage)
	}

	return messages
}

// ==================== Streaming Execution Methods ====================

// executeStreamWithIterations executes streaming iterations (supports multi-round tool calling)
func (ae *AgentEngine) executeStreamWithIterations(initialMessages []types.Message, resultChan chan<- StreamResult) {
	messages := initialMessages
	finalResult := &AgentResult{}

	ae.mu.RLock()
	maxIterations := 10
	if ae.config != nil {
		maxIterations = ae.config.MaxIterations
	}
	ae.mu.RUnlock()

	estimatedToolCalls := maxIterations * 3
	toolCalls := make([]types.ToolCallRequest, 0, estimatedToolCalls)
	intermediateSteps := make([]types.ToolCallData, 0, estimatedToolCalls)

	for iteration := 0; iteration < maxIterations; iteration++ {
		iterationStartTime := time.Now()
		ae.logger.LogExecution("executeStreamWithIterations", iteration,
			fmt.Sprintf("Starting streaming iteration %d/%d", iteration+1, maxIterations))

		// Execute single round iteration with streaming
		iterationResult, hasMore, err := ae.executeStreamIteration(messages, resultChan, iteration)
		if err != nil {
			ae.logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1))
			resultChan <- StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_STREAM_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err),
			}
			return
		}

		// Accumulate final result
		finalResult.Output = iterationResult.Output
		toolCalls = append(toolCalls, iterationResult.ToolCalls...)
		intermediateSteps = append(intermediateSteps, iterationResult.IntermediateSteps...)

		// If no more tool calls, end iteration
		if !hasMore {
			ae.logger.LogExecution("executeStreamWithIterations", iteration,
				"Streaming execution completed",
				slog.Int("total_iterations", iteration+1),
				slog.Duration("iteration_duration", time.Since(iterationStartTime)))
			break
		}

		if iteration+1 < maxIterations {
			ae.logger.LogExecution("executeStreamWithIterations", iteration, "Preparing next iteration messages")
			messages = ae.buildNextMessages(messages, iterationResult)
		} else {
			ae.logger.LogExecution("executeStreamWithIterations", iteration, "Reached maximum iterations")
		}
	}

	// Save to memory system
	if ae.memory != nil && len(initialMessages) > 0 {
		input := map[string]interface{}{"input": initialMessages[len(initialMessages)-1].Content}
		output := map[string]interface{}{"output": finalResult.Output}
		if err := ae.memory.SaveContext(input, output); err != nil {
			ae.logger.LogError("executeStreamWithIterations", err, slog.String("phase", "save_context"))
			// Do not interrupt execution as main flow is complete
		} else {
			// Check if memory compression is needed
			ae.mu.RLock()
			enableCompress := false
			compressThreshold := 0
			if ae.config != nil {
				enableCompress = ae.config.EnableMemoryCompress
				compressThreshold = ae.config.MemoryCompressThreshold
			}
			ae.mu.RUnlock()

			if enableCompress && compressThreshold > 0 {
				history, err := ae.memory.GetChatHistory()
				if err == nil && len(history) > compressThreshold {
					ae.mu.RLock()
					llm := ae.model
					ae.mu.RUnlock()
					if llm != nil {
						if err := ae.memory.CompressMemory(llm, compressThreshold); err != nil {
							ae.logger.LogError("executeStreamWithIterations", err, slog.String("phase", "compress_memory"))
						} else {
							ae.logger.Info("Memory compressed successfully",
								slog.Int("original_count", len(history)),
								slog.Int("threshold", compressThreshold))
						}
					}
				}
			}
		}
	}

	// Set final result's tool calls and intermediate steps
	finalResult.ToolCalls = toolCalls
	finalResult.IntermediateSteps = intermediateSteps

	ae.logger.LogExecution("executeStreamWithIterations", 0, "Stream execution completed successfully",
		slog.Int("total_iterations", len(toolCalls)),
		slog.Int("total_tools", len(toolCalls)))

	resultChan <- StreamResult{
		Type:   "end",
		Result: finalResult,
	}
}

// executeStreamIteration executes a single streaming iteration
// Processes one round of streaming LLM calling and tool execution, supporting real-time content delivery
// Parameters:
//   - messages: current round messages
//   - resultChan: streaming result channel
//   - iteration: current iteration index
//
// Returns:
//   - execution result
//   - whether to continue iteration
//   - error information
func (ae *AgentEngine) executeStreamIteration(messages []types.Message, resultChan chan<- StreamResult, iteration int) (*AgentResult, bool, error) {
	result := &AgentResult{}

	ae.mu.RLock()
	tools := ae.tools
	maxIterations := 10
	timeout := time.Duration(0)
	toolExecutionTimeout := time.Duration(0)
	if ae.config != nil {
		maxIterations = ae.config.MaxIterations
		timeout = ae.config.Timeout
		toolExecutionTimeout = ae.config.ToolExecutionTimeout
	}
	ctx := ae.ctx
	ae.mu.RUnlock()

	// Create context with timeout if configured
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if ae.model == nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "LLM model provider is nil")
	}

	stream, err := ae.model.ChatWithToolsStream(messages, tools)
	if err != nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "failed to chat with tools stream").Wrap(err)
	}

	intermediateSteps := []types.ToolCallData{}
	var outputBuilder strings.Builder
	outputBuilder.Grow(2048)

	for msg := range stream {
		switch msg.Type {
		case "chunk":
			outputBuilder.WriteString(msg.Content)
			resultChan <- StreamResult{
				Type:    "chunk",
				Content: msg.Content,
			}
		case "tool_calls":
			for _, tc := range msg.ToolCalls {
				result.ToolCalls = append(result.ToolCalls, types.ToolCallRequest{
					Tool:       tc.Function.Name,
					ToolInput:  tc.Function.Arguments,
					ToolCallID: tc.ID,
					Type:       tc.Type,
				})
			}
		case "error":
			return nil, false, errors.NewError(errors.EC_STREAM_ERROR.Code, "stream error occurred").Wrap(fmt.Errorf("%s", msg.Error))
		}
	}

	result.Output = outputBuilder.String()

	if len(result.ToolCalls) > 0 {
		ae.logger.LogExecution("executeStreamIteration", iteration, "Processing tool calls",
			slog.Int("tool_count", len(result.ToolCalls)))

		if iteration+1 >= maxIterations {
			ae.logger.LogExecution("executeStreamIteration", iteration, "Reached maximum iterations, skipping tool execution")
			return result, false, nil
		}

		// Convert ToolCallRequest to ToolCall for sorting
		toolCallsForSorting := make([]types.ToolCall, 0, len(result.ToolCalls))
		for _, tc := range result.ToolCalls {
			toolCallsForSorting = append(toolCallsForSorting, types.ToolCall{
				ID:   tc.ToolCallID,
				Type: tc.Type,
				Function: types.ToolFunction{
					Name:      tc.Tool,
					Arguments: tc.ToolInput,
				},
			})
		}

		// Sort tool calls by priority and dependencies
		sortedToolCalls, err := ae.sortToolCallsByDependencies(toolCallsForSorting)
		if err != nil {
			ae.logger.LogError("executeStreamIteration", err, slog.String("phase", "sort_tool_calls"))
			// Continue with original order if sorting fails
			sortedToolCalls = toolCallsForSorting
		}

		// Convert back to ToolCallRequest
		sortedToolCallRequests := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
		for _, tc := range sortedToolCalls {
			sortedToolCallRequests = append(sortedToolCallRequests, types.ToolCallRequest{
				Tool:       tc.Function.Name,
				ToolInput:  tc.Function.Arguments,
				ToolCallID: tc.ID,
				Type:       tc.Type,
			})
		}

		for _, toolCall := range sortedToolCallRequests {
			ae.logger.LogExecution("executeStreamIteration", iteration, "Executing tool",
				slog.String("tool_name", toolCall.Tool))

			ae.mu.RLock()
			tool, exists := ae.toolsMap[toolCall.Tool]
			ae.mu.RUnlock()
			if !exists {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", toolCall.Tool)
				ae.logger.LogError("executeStreamIteration", fmt.Errorf("tool %q not found in available tools", toolCall.Tool),
					slog.String("tool_name", toolCall.Tool))
				intermediateSteps = append(intermediateSteps, types.ToolCallData{
					Action: types.ToolActionStep{
						Tool:       toolCall.Tool,
						ToolInput:  toolCall.ToolInput,
						ToolCallID: toolCall.ToolCallID,
						Type:       toolCall.Type,
					},
					Observation: errMsg,
				})
				continue
			}

			// Check cache first
			toolStartTime := time.Now()
			toolResult, err, cached := ae.getCachedToolResult(toolCall.Tool, toolCall.ToolInput)
			if cached {
				ae.logger.LogToolExecution(toolCall.Tool, true, 0, slog.Bool("cached", true), slog.String("context", "streaming"))
				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed (cached error): %v", toolCall.Tool, err)
					ae.logger.LogToolExecution(toolCall.Tool, false, 0,
						slog.String("error", err.Error()),
						slog.Bool("cached", true),
						slog.String("context", "streaming"))
					intermediateSteps = append(intermediateSteps, types.ToolCallData{
						Action: types.ToolActionStep{
							Tool:       toolCall.Tool,
							ToolInput:  toolCall.ToolInput,
							ToolCallID: toolCall.ToolCallID,
							Type:       toolCall.Type,
						},
						Observation: errMsg,
					})
					continue
				}
			} else {
				// Execute tool with timeout
				toolResult, err = ae.executeToolWithTimeout(tool, toolCall.ToolInput, toolExecutionTimeout)
				duration := time.Since(toolStartTime)

				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed: %v", toolCall.Tool, err)
					ae.logger.LogToolExecution(toolCall.Tool, false, duration,
						slog.String("error", err.Error()),
						slog.String("tool_input", fmt.Sprintf("%v", toolCall.ToolInput)),
						slog.String("context", "streaming"))
					intermediateSteps = append(intermediateSteps, types.ToolCallData{
						Action: types.ToolActionStep{
							Tool:       toolCall.Tool,
							ToolInput:  toolCall.ToolInput,
							ToolCallID: toolCall.ToolCallID,
							Type:       toolCall.Type,
						},
						Observation: errMsg,
					})
					continue
				}

				// Cache tool result
				ae.setCachedToolResult(toolCall.Tool, toolCall.ToolInput, toolResult, err)
				ae.logger.LogToolExecution(toolCall.Tool, true, duration, slog.Bool("cached", false), slog.String("context", "streaming"))
			}

			// Format observation from tool result
			truncationLength := ae.getToolTruncationLength(toolCall.Tool)
			observation := truncateString(formatToolResult(toolResult), truncationLength)

			intermediateSteps = append(intermediateSteps, types.ToolCallData{
				Action: types.ToolActionStep{
					Tool:       toolCall.Tool,
					ToolInput:  toolCall.ToolInput,
					ToolCallID: toolCall.ToolCallID,
					Type:       toolCall.Type,
				},
				Observation: observation,
			})
		}

		result.IntermediateSteps = intermediateSteps

		ae.logger.LogExecution("executeStreamIteration", iteration, "Tool execution completed",
			slog.Int("executed_tools", len(result.ToolCalls)),
			slog.Int("intermediate_steps", len(intermediateSteps)))

		return result, len(result.ToolCalls) > 0, nil
	}

	ae.logger.LogExecution("executeStreamIteration", iteration, "No tool calls in this iteration")
	return result, false, nil
}

// ==================== Tool Execution Methods ====================

// executeToolWithTimeout executes a tool with timeout control
// Uses goroutine + channel to implement timeout without modifying Tool interface
// Note: The goroutine will continue running after timeout, but will naturally complete.
// This is an acceptable trade-off since the Tool interface doesn't support context cancellation.
// The goroutine will finish and clean up resources automatically, preventing leaks.
func (ae *AgentEngine) executeToolWithTimeout(tool types.Tool, args map[string]interface{}, timeout time.Duration) (interface{}, error) {
	if timeout <= 0 {
		// No timeout, execute directly
		return tool.Execute(args)
	}

	type result struct {
		value interface{}
		err   error
	}

	resultChan := make(chan result, 1)
	// Use buffered channel to prevent goroutine leak if timeout occurs
	go func() {
		var value interface{}
		var err error

		defer func() {
			// Recover from any panic in tool execution
			if r := recover(); r != nil {
				err = fmt.Errorf("tool execution panic: %v", r)
				value = nil
			}
			// Non-blocking send (buffered channel)
			// This ensures we always try to send the result, even if timeout occurred
			select {
			case resultChan <- result{value: value, err: err}:
			default:
				// Channel already closed or receiver gone (timeout occurred), ignore
			}
		}()

		value, err = tool.Execute(args)
	}()

	select {
	case res := <-resultChan:
		return res.value, res.err
	case <-time.After(timeout):
		// Timeout occurred, but goroutine will continue and complete naturally
		// This is acceptable since tool interface doesn't support cancellation
		return nil, errors.EC_TOOL_EXECUTION_TIMEOUT.Wrap(fmt.Errorf("tool execution timeout after %v", timeout))
	}
}

// ==================== Cache Management Methods ====================

// generateToolCacheKey generates a tool cache key
// Generates a unique cache key based on tool name and parameters
// Uses tool name prefix to reduce collision probability
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - cache key string
func generateToolCacheKey(toolName string, args map[string]interface{}) string {
	hasher := md5.New()
	// Include tool name in hash to reduce collision probability
	hasher.Write([]byte("tool:" + toolName + ":"))

	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			// If marshaling fails, use a fallback to ensure cache key uniqueness
			// This prevents cache collisions when args contain non-marshalable types
			hasher.Write([]byte(fmt.Sprintf("fallback:%v", args)))
		} else {
			hasher.Write(argsJSON)
		}
	}

	return toolName + ":" + hex.EncodeToString(hasher.Sum(nil))
}

// getToolTruncationLength gets truncation length for a tool
// Returns tool-specific truncation length from metadata, or default if not set
// Parameters:
//   - toolName: tool name
//
// Returns:
//   - truncation length
func (ae *AgentEngine) getToolTruncationLength(toolName string) int {
	ae.mu.RLock()
	tool, exists := ae.toolsMap[toolName]
	ae.mu.RUnlock()

	if exists {
		metadata := tool.Metadata()
		if metadata.MaxTruncationLength > 0 {
			return metadata.MaxTruncationLength
		}
	}
	return MaxTruncationLength
}

// getCachedToolResult gets cached tool result
// Retrieves tool execution result from cache to avoid repeated execution
// Updates LRU order on cache hit
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - tool execution result
//   - execution error (if any)
//   - whether cache was found
func (ae *AgentEngine) getCachedToolResult(toolName string, args map[string]interface{}) (interface{}, error, bool) {
	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	entry, exists := ae.toolCache[cacheKey]
	if !exists {
		return nil, nil, false
	}

	// Check expiration
	if time.Since(entry.timestamp) >= CacheExpirationTime {
		ae.removeCacheEntry(entry)
		return nil, nil, false
	}

	// Move to head (most recently used)
	ae.moveToHead(entry)
	return entry.result, entry.err, true
}

// setCachedToolResult sets tool result cache
// Caches tool execution result to avoid repeated execution of the same tool call
// Uses LRU eviction strategy: removes expired entries first, then least recently used entries
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//   - result: tool execution result
//   - err: execution error (if any)
func (ae *AgentEngine) setCachedToolResult(toolName string, args map[string]interface{}, result interface{}, err error) {
	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	// Check if entry already exists (update existing entry)
	if existing, exists := ae.toolCache[cacheKey]; exists {
		existing.result = result
		existing.err = err
		existing.timestamp = time.Now()
		ae.moveToHead(existing)
		return
	}

	// Remove expired entries first
	ae.removeExpiredEntries()

	// If cache is still full, remove least recently used entries (from tail)
	for len(ae.toolCache) >= ae.toolCacheSize && ae.toolCacheTail != nil {
		ae.removeCacheEntry(ae.toolCacheTail)
	}

	// Create new entry and add to head
	entry := &toolCacheEntry{
		result:    result,
		err:       err,
		timestamp: time.Now(),
		key:       cacheKey,
	}
	ae.toolCache[cacheKey] = entry
	ae.addToHead(entry)
}

// removeExpiredEntries removes all expired cache entries
func (ae *AgentEngine) removeExpiredEntries() {
	now := time.Now()
	current := ae.toolCacheTail
	for current != nil {
		next := current.prev
		if now.Sub(current.timestamp) >= CacheExpirationTime {
			ae.removeCacheEntry(current)
		}
		current = next
	}
}

// addToHead adds an entry to the head of LRU list
func (ae *AgentEngine) addToHead(entry *toolCacheEntry) {
	entry.prev = nil
	entry.next = ae.toolCacheHead

	if ae.toolCacheHead != nil {
		ae.toolCacheHead.prev = entry
	} else {
		ae.toolCacheTail = entry
	}
	ae.toolCacheHead = entry
}

// moveToHead moves an existing entry to the head of LRU list
func (ae *AgentEngine) moveToHead(entry *toolCacheEntry) {
	if entry == ae.toolCacheHead {
		return
	}

	// Remove from current position
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		ae.toolCacheTail = entry.prev
	}

	// Add to head
	entry.prev = nil
	entry.next = ae.toolCacheHead
	if ae.toolCacheHead != nil {
		ae.toolCacheHead.prev = entry
	}
	ae.toolCacheHead = entry
}

// removeCacheEntry removes an entry from cache and LRU list
func (ae *AgentEngine) removeCacheEntry(entry *toolCacheEntry) {
	if entry == nil {
		return
	}

	delete(ae.toolCache, entry.key)

	if entry.prev != nil {
		entry.prev.next = entry.next
	} else {
		ae.toolCacheHead = entry.next
	}

	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		ae.toolCacheTail = entry.prev
	}

	entry.prev = nil
	entry.next = nil
}

// ==================== Tool Dependency Management Methods ====================

// sortToolCallsByDependencies sorts tool calls by priority and dependencies using topological sort
// Returns sorted tool calls and error if circular dependency is detected
func (ae *AgentEngine) sortToolCallsByDependencies(toolCalls []types.ToolCall) ([]types.ToolCall, error) {
	if len(toolCalls) <= 1 {
		return toolCalls, nil
	}

	ae.mu.RLock()
	toolsMap := make(map[string]types.Tool, len(ae.toolsMap))
	for k, v := range ae.toolsMap {
		toolsMap[k] = v
	}
	ae.mu.RUnlock()

	// Build dependency graph and priority map
	dependencyGraph := make(map[string][]string)   // tool -> dependencies
	priorityMap := make(map[string]int)            // tool -> priority
	toolCallMap := make(map[string]types.ToolCall) // tool name -> tool call

	for _, tc := range toolCalls {
		toolName := tc.Function.Name
		toolCallMap[toolName] = tc

		// Get tool metadata
		if tool, exists := toolsMap[toolName]; exists {
			metadata := tool.Metadata()
			priorityMap[toolName] = metadata.Priority
			if len(metadata.Dependencies) > 0 {
				dependencyGraph[toolName] = metadata.Dependencies
			}
		} else {
			priorityMap[toolName] = 0
		}
	}

	// Detect circular dependencies
	if err := ae.detectCircularDependencies(dependencyGraph); err != nil {
		return nil, err
	}

	// Topological sort with priority
	sorted := make([]types.ToolCall, 0, len(toolCalls))
	visited := make(map[string]bool)
	inProgress := make(map[string]bool)

	var visit func(string) error
	visit = func(toolName string) error {
		if inProgress[toolName] {
			return fmt.Errorf("circular dependency detected involving tool: %s", toolName)
		}
		if visited[toolName] {
			return nil
		}

		inProgress[toolName] = true

		// Visit dependencies first
		if deps, hasDeps := dependencyGraph[toolName]; hasDeps {
			for _, dep := range deps {
				if _, exists := toolCallMap[dep]; exists {
					if err := visit(dep); err != nil {
						return err
					}
				}
			}
		}

		inProgress[toolName] = false
		visited[toolName] = true

		// Add to sorted list
		if tc, exists := toolCallMap[toolName]; exists {
			sorted = append(sorted, tc)
		}

		return nil
	}

	// Sort by priority first, then visit
	type toolWithPriority struct {
		toolCall types.ToolCall
		priority int
	}
	toolsWithPriority := make([]toolWithPriority, 0, len(toolCalls))
	for _, tc := range toolCalls {
		toolsWithPriority = append(toolsWithPriority, toolWithPriority{
			toolCall: tc,
			priority: priorityMap[tc.Function.Name],
		})
	}

	// Sort by priority (descending) using efficient sort.Slice
	sort.Slice(toolsWithPriority, func(i, j int) bool {
		return toolsWithPriority[i].priority > toolsWithPriority[j].priority
	})

	// Visit tools in priority order
	for _, twp := range toolsWithPriority {
		if err := visit(twp.toolCall.Function.Name); err != nil {
			return nil, err
		}
	}

	return sorted, nil
}

// detectCircularDependencies detects circular dependencies in the dependency graph
func (ae *AgentEngine) detectCircularDependencies(graph map[string][]string) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(toolName string) bool {
		visited[toolName] = true
		recStack[toolName] = true

		if deps, exists := graph[toolName]; exists {
			for _, dep := range deps {
				if !visited[dep] {
					if hasCycle(dep) {
						return true
					}
				} else if recStack[dep] {
					return true
				}
			}
		}

		recStack[toolName] = false
		return false
	}

	for toolName := range graph {
		if !visited[toolName] {
			if hasCycle(toolName) {
				return fmt.Errorf("circular dependency detected in tool dependencies")
			}
		}
	}

	return nil
}

// ==================== Lifecycle Management Methods ====================

// Stop stops the agent engine
// Safely stops the agent engine and releases resources
func (ae *AgentEngine) Stop() {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	if ae.cancel != nil {
		ae.cancel()
	}
	ae.isRunning.Store(false)
}
