package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	toolCache     map[string]toolCacheEntry // Tool execution result cache
	toolCacheMu   sync.RWMutex              // Cache read-write lock
	toolCacheSize int                       // Cache size limit
}

// NewAgentEngine creates a new agent engine
// Parameters:
//   - model: LLM model provider
//   - config: agent configuration
//
// Returns:
//   - initialized AgentEngine instance
func NewAgentEngine(model types.LLMProvider, config *types.AgentConfig) *AgentEngine {
	ctx, cancel := context.WithCancel(context.Background())

	return &AgentEngine{
		model:         model,
		config:        config,
		tools:         make([]types.Tool, 0),
		toolsMap:      make(map[string]types.Tool),
		toolCache:     make(map[string]toolCacheEntry),
		toolCacheSize: DefaultCacheSize, // Using constant-defined cache size
		logger:        logger.NewLogger(),
		ctx:           ctx,
		cancel:        cancel,
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
	ae.config = config
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

	// Pre-allocate slice capacity to reduce memory reallocations
	messages, err := ae.prepareMessages(input, previousRequests)
	if err != nil {
		ae.logger.LogError("Execute", err, slog.String("phase", "prepare_messages"))
		return nil, errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, errors.EC_PREPARE_MESSAGES_FAILED.Message).Wrap(err)
	}

	var finalResult *AgentResult
	iteration := 0
	ae.mu.RLock()
	maxIterations := ae.config.MaxIterations
	ae.mu.RUnlock()

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
	ae.logger.LogExecution("Execute", 0, "Agent execution completed successfully",
		slog.Duration("total_duration", executionTime),
		slog.Int("total_iterations", iteration+1),
		slog.Int("output_length", len(finalResult.Output)))

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
			enableCompress := ae.config.EnableMemoryCompress
			compressThreshold := ae.config.MemoryCompressThreshold
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

		defer func() {
			if r := recover(); r != nil {
				ae.logger.LogError("ExecuteStream", fmt.Errorf("panic recovered: %v", r))
				resultChan <- StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_STREAM_PANIC.Code, "panic in stream execution").Wrap(fmt.Errorf("%v", r)),
				}
			}
		}()

		// 准备初始消息
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
	context := "Previous tool calls:\n"
	for _, req := range requests {
		context += fmt.Sprintf("Tool: %s, Input: %v, Result: %s\n",
			req.Action.Tool, req.Action.ToolInput, req.Observation)
	}
	return context
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
	maxIterations := ae.config.MaxIterations
	ae.mu.RUnlock()
	startTime := time.Now()
	ae.logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

	ae.mu.RLock()
	tools := ae.tools
	ctx := ae.ctx
	ae.mu.RUnlock()

	// Create context with timeout if configured
	if ctx == nil {
		ctx = context.Background()
	}
	ae.mu.RLock()
	timeout := ae.config.Timeout
	ae.mu.RUnlock()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
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
				ae.logger.Info("Tool not found",
					slog.String("tool_name", toolCall.Function.Name),
					slog.Int("iteration", iteration+1))
				continue
			}

			// Check cache
			toolStartTime := time.Now()
			toolResult, err, cached := ae.getCachedToolResult(toolCall.Function.Name, toolCall.Function.Arguments)
			if cached {
				ae.logger.LogToolExecution(toolCall.Function.Name, true, 0, slog.Bool("cached", true))
			} else {
				// Execute tool
				toolResult, err = tool.Execute(toolCall.Function.Arguments)
				duration := time.Since(toolStartTime)

				if err != nil {
					ae.logger.LogToolExecution(toolCall.Function.Name, false, duration, slog.String("error", err.Error()))
					intermediateSteps = append(intermediateSteps, types.ToolCallData{
						Action: types.ToolActionStep{
							Tool:       toolCall.Function.Name,
							ToolInput:  toolCall.Function.Arguments,
							ToolCallID: toolCall.ID,
							Type:       toolCall.Type,
						},
						Observation: fmt.Sprintf("Tool execution failed: %v", err),
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

			intermediateSteps = append(intermediateSteps, types.ToolCallData{
				Action: types.ToolActionStep{
					Tool:       toolCall.Function.Name,
					ToolInput:  toolCall.Function.Arguments,
					ToolCallID: toolCall.ID,
					Type:       toolCall.Type,
				},
				Observation: fmt.Sprintf("%v", toolResult),
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
	if len(result.IntermediateSteps) > 0 {
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
	maxIterations := ae.config.MaxIterations
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
			enableCompress := ae.config.EnableMemoryCompress
			compressThreshold := ae.config.MemoryCompressThreshold
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
	maxIterations := ae.config.MaxIterations
	ctx := ae.ctx
	ae.mu.RUnlock()

	// Create context with timeout if configured
	if ctx == nil {
		ctx = context.Background()
	}
	ae.mu.RLock()
	timeout := ae.config.Timeout
	ae.mu.RUnlock()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	stream, err := ae.model.ChatWithToolsStream(messages, tools)
	if err != nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "failed to chat with tools stream").Wrap(err)
	}

	intermediateSteps := []types.ToolCallData{}

	for msg := range stream {
		switch msg.Type {
		case "chunk":
			result.Output += msg.Content
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

		// Pre-allocate result slices
		toolResults := make([]interface{}, 0, len(sortedToolCallRequests))
		toolErrors := make([]error, 0, len(sortedToolCallRequests))

		for _, toolCall := range sortedToolCallRequests {
			ae.logger.LogExecution("executeStreamIteration", iteration, "Executing tool",
				slog.String("tool_name", toolCall.Tool))

			ae.mu.RLock()
			tool, exists := ae.toolsMap[toolCall.Tool]
			ae.mu.RUnlock()
			if !exists {
				ae.logger.LogError("executeStreamIteration", fmt.Errorf("tool not found"),
					slog.String("tool_name", toolCall.Tool))
				continue
			}

			// Check cache first
			toolStartTime := time.Now()
			toolResult, err, cached := ae.getCachedToolResult(toolCall.Tool, toolCall.ToolInput)
			if cached {
				ae.logger.LogToolExecution(toolCall.Tool, true, 0, slog.Bool("cached", true), slog.String("context", "streaming"))
			} else {
				// Execute tool if not cached
				toolResult, err = tool.Execute(toolCall.ToolInput)
				duration := time.Since(toolStartTime)

				if err != nil {
					ae.logger.LogToolExecution(toolCall.Tool, false, duration, slog.String("error", err.Error()), slog.String("context", "streaming"))
					toolErrors = append(toolErrors, err)
					toolResults = append(toolResults, nil)
					continue
				}

				// Cache tool result
				ae.setCachedToolResult(toolCall.Tool, toolCall.ToolInput, toolResult, err)
				ae.logger.LogToolExecution(toolCall.Tool, true, duration, slog.Bool("cached", false), slog.String("context", "streaming"))
			}

			toolResults = append(toolResults, toolResult)
			toolErrors = append(toolErrors, err)

			// Use truncated result string
			observation := truncateString(fmt.Sprintf("%v", toolResult), MaxTruncationLength)
			if err != nil {
				observation = fmt.Sprintf("Tool execution failed: %v", err)
			}

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
			slog.Int("failed_tools", len(toolErrors)))

		return result, len(result.ToolCalls) > 0, nil
	}

	ae.logger.LogExecution("executeStreamIteration", iteration, "No tool calls in this iteration")
	return result, false, nil
}

// ==================== Cache Management Methods ====================

// generateToolCacheKey generates a tool cache key
// Generates a unique cache key based on tool name and parameters
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - cache key string
func generateToolCacheKey(toolName string, args map[string]interface{}) string {
	hasher := md5.New()
	hasher.Write([]byte(toolName))

	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err == nil {
			hasher.Write(argsJSON)
		}
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// getCachedToolResult gets cached tool result
// Retrieves tool execution result from cache to avoid repeated execution
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - tool execution result
//   - execution error (if any)
//   - whether cache was found
func (ae *AgentEngine) getCachedToolResult(toolName string, args map[string]interface{}) (interface{}, error, bool) {
	ae.mu.RLock()
	enableToolRetry := ae.config.EnableToolRetry
	ae.mu.RUnlock()
	if !enableToolRetry {
		return nil, nil, false
	}

	// Use read-write lock to improve concurrent performance
	ae.toolCacheMu.RLock()
	entry, exists := ae.toolCache[generateToolCacheKey(toolName, args)]
	ae.toolCacheMu.RUnlock()

	if exists && time.Since(entry.timestamp) < CacheExpirationTime {
		return entry.result, entry.err, true
	}
	return nil, nil, false
}

// setCachedToolResult sets tool result cache
// Caches tool execution result to avoid repeated execution of the same tool call
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//   - result: tool execution result
//   - err: execution error (if any)
func (ae *AgentEngine) setCachedToolResult(toolName string, args map[string]interface{}, result interface{}, err error) {
	ae.mu.RLock()
	enableToolRetry := ae.config.EnableToolRetry
	ae.mu.RUnlock()
	if !enableToolRetry {
		return
	}

	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	// Simple LRU strategy: if cache is full, remove expired entries first, then oldest entry
	if len(ae.toolCache) >= ae.toolCacheSize {
		now := time.Now()
		expiredKeys := make([]string, 0, len(ae.toolCache)/4)
		var oldestKey string
		var oldestTime time.Time
		removedCount := 0
		maxRemovals := len(ae.toolCache) / 4

		// First pass: collect expired entries and find oldest (limit iterations)
		for key, entry := range ae.toolCache {
			if removedCount >= maxRemovals {
				break
			}
			if now.Sub(entry.timestamp) >= CacheExpirationTime {
				expiredKeys = append(expiredKeys, key)
				removedCount++
			} else if oldestKey == "" || entry.timestamp.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.timestamp
			}
		}

		// Remove expired entries first
		for _, key := range expiredKeys {
			delete(ae.toolCache, key)
		}

		// If still full after removing expired, remove oldest
		if len(ae.toolCache) >= ae.toolCacheSize && oldestKey != "" {
			delete(ae.toolCache, oldestKey)
		}
	}

	ae.toolCache[cacheKey] = toolCacheEntry{
		result:    result,
		err:       err,
		timestamp: time.Now(),
	}
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

	// Sort by priority (descending)
	for i := 0; i < len(toolsWithPriority)-1; i++ {
		for j := i + 1; j < len(toolsWithPriority); j++ {
			if toolsWithPriority[i].priority < toolsWithPriority[j].priority {
				toolsWithPriority[i], toolsWithPriority[j] = toolsWithPriority[j], toolsWithPriority[i]
			}
		}
	}

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
