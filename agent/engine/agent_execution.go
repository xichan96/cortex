package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

// ==================== Core Execution Methods ====================

// Execute executes the agent task (supports multi-round iteration)
// Processes user input with tool calling and multi-round iteration, returning the complete execution result
// Parameters:
//   - ctx: context for cancellation
//   - input: user input
//   - previousRequests: previous tool call request history
//
// Returns:
//   - execution result containing output, tool calls, and intermediate steps
//   - error information
func (ae *AgentEngine) Execute(ctx context.Context, input types.AgentInput, previousRequests []types.ToolCallData) (*types.AgentResult, error) {
	if !ae.isRunning.CompareAndSwap(false, true) {
		return nil, errors.EC_AGENT_BUSY
	}

	defer ae.isRunning.Store(false)

	// Add execution tracking
	startTime := time.Now()
	logger.LogExecution("Execute", 0, "Starting agent execution",
		slog.String("input", types.TruncateString(input.String(), 100)),
		slog.Int("previousRequests", len(previousRequests)))

	ae.mu.RLock()
	limiter := ae.rateLimiter
	ae.mu.RUnlock()

	if limiter != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		// Create a separate context for rate limiter to avoid cancelling the main context
		limitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := limiter.Wait(limitCtx); err != nil {
			logger.LogError("Execute", err, slog.String("phase", "rate_limit"))
			return nil, errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err)
		}
	}

	// Pre-allocate slice capacity to reduce memory reallocations
	messages, err := ae.prepareMessages(ctx, input, previousRequests)
	if err != nil {
		logger.LogError("Execute", err, slog.String("phase", "prepare_messages"))
		return nil, errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, errors.EC_PREPARE_MESSAGES_FAILED.Message).Wrap(err)
	}

	var finalResult *types.AgentResult
	iteration := 0
	ae.mu.RLock()
	maxIterations := 10
	if ae.config != nil {
		maxIterations = ae.config.MaxIterations
	}
	ae.mu.RUnlock()

	// Initialize finalResult to prevent nil pointer panic
	finalResult = &types.AgentResult{Output: ""}
	totalUsage := types.Usage{}

	// Iterate until no tool calls or maximum iterations reached
	for iteration < maxIterations {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		logger.LogExecution("Execute", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

		// Execute single iteration
		result, continueIterating, err := ae.executeIteration(ctx, messages, iteration)
		if err != nil {
			logger.LogError("Execute", err, slog.Int("iteration", iteration+1))
			return nil, errors.NewError(errors.EC_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err)
		}

		// Accumulate usage
		totalUsage.PromptTokens += result.Usage.PromptTokens
		totalUsage.CompletionTokens += result.Usage.CompletionTokens
		totalUsage.TotalTokens += result.Usage.TotalTokens

		// Save final result
		finalResult = result
		finalResult.Usage = totalUsage

		// If no tool calls or continuation not needed, end
		if !continueIterating || len(result.ToolCalls) == 0 {
			logger.LogExecution("Execute", iteration, "Execution completed, no more tool calls")
			break
		}

		// Prepare next round messages
		messages = ae.buildNextMessages(messages, result)
		iteration++

		// Avoid too fast execution - only delay if there are more iterations
		if iteration < maxIterations {
			logger.LogExecution("Execute", iteration, "Preparing next iteration")
			time.Sleep(types.IterationDelay)
		} else {
			logger.LogExecution("Execute", iteration, "Reached maximum iterations")
		}
	}

	if iteration >= maxIterations {
		logger.LogExecution("Execute", iteration, fmt.Sprintf("Reached maximum iteration limit: %d", maxIterations))
	}

	executionTime := time.Since(startTime)
	outputLength := 0
	if finalResult != nil {
		outputLength = len(finalResult.Output)
	}
	logger.LogExecution("Execute", 0, "Agent execution completed successfully",
		slog.Duration("total_duration", executionTime),
		slog.Int("total_iterations", iteration+1),
		slog.Int("output_length", outputLength))

	// Save to memory system
	if ae.memory != nil && finalResult != nil {
		ae.mu.RLock()
		chatRole := ""
		if ae.config != nil {
			chatRole = ae.config.ChatMessageRole
		}
		ae.mu.RUnlock()

		if chatRole == "" {
			chatRole = "user"
		}
		inputMap := map[string]interface{}{
			"input": input.String(),
			"role":  chatRole,
			"parts": input.Parts,
		}
		outputMap := map[string]interface{}{"output": finalResult.Output}
		if err := ae.memory.SaveContext(ctx, inputMap, outputMap); err != nil {
			logger.LogError("Execute", err, slog.String("phase", "save_context"))
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
				history, err := ae.memory.GetChatHistory(ctx)
				if err == nil && len(history) > compressThreshold {
					ae.mu.RLock()
					llm := ae.model
					ae.mu.RUnlock()
					if llm != nil {
						// Execute compression asynchronously to avoid blocking the main thread
						go func() {
							defer func() {
								if r := recover(); r != nil {
									logger.LogError("Execute", fmt.Errorf("panic in compress memory async: %v", r))
								}
							}()
							if err := ae.memory.CompressMemory(context.Background(), llm, compressThreshold); err != nil {
								logger.LogError("Execute", err, slog.String("phase", "compress_memory_async"))
							} else {
								logger.Info("Memory compressed successfully",
									slog.Int("original_count", len(history)),
									slog.Int("threshold", compressThreshold))
							}
						}()
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
//   - ctx: context for cancellation
//   - input: user input
//   - previousRequests: previous tool call request history
//
// Returns:
//   - streaming result channel for real-time content delivery during execution
//   - error information (only during initialization)
func (ae *AgentEngine) ExecuteStream(ctx context.Context, input types.AgentInput, previousRequests []types.ToolCallData) (<-chan types.StreamResult, error) {
	if !ae.isRunning.CompareAndSwap(false, true) {
		return nil, errors.EC_AGENT_BUSY
	}

	resultChan := make(chan types.StreamResult, types.DefaultChannelBuffer)

	go func() {
		defer close(resultChan)
		defer ae.isRunning.Store(false)

		startTime := time.Now()
		logger.LogExecution("ExecuteStream", 0, "Starting stream execution", slog.String("input", types.TruncateString(input.String(), 100)), slog.Int("previousRequests", len(previousRequests)))

		ae.mu.RLock()
		limiter := ae.rateLimiter
		ae.mu.RUnlock()

		if limiter != nil {
			if ctx == nil {
				ctx = context.Background()
			}
			// Create a separate context for rate limiter
			limitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := limiter.Wait(limitCtx); err != nil {
				logger.LogError("ExecuteStream", err, slog.String("phase", "rate_limit"))
				resultChan <- types.StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err),
				}
				return
			}
		}

		defer func() {
			if r := recover(); r != nil {
				logger.LogError("ExecuteStream", fmt.Errorf("panic recovered: %v", r))
				resultChan <- types.StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_STREAM_PANIC.Code, "panic in stream execution").Wrap(fmt.Errorf("%v", r)),
				}
			}
		}()

		// Prepare initial messages
		messages, err := ae.prepareMessages(ctx, input, previousRequests)
		if err != nil {
			logger.LogError("ExecuteStream", err, slog.String("phase", "prepare_messages"))
			resultChan <- types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, "failed to prepare messages").Wrap(err),
			}
			return
		}

		// Stream iterative execution
		ae.executeStreamWithIterations(ctx, messages, resultChan)

		logger.LogExecution("ExecuteStream", 0, "Stream execution completed", slog.Duration("total_duration", time.Since(startTime)))
	}()

	return resultChan, nil
}

// prepareMessages prepares messages
// Builds a complete message list including system messages, chat history, tool call context, and user input
// Parameters:
//   - ctx: context
//   - input: user input
//   - previousRequests: previous tool call requests
//
// Returns:
//   - built message list
//   - error information
func (ae *AgentEngine) prepareMessages(ctx context.Context, input types.AgentInput, previousRequests []types.ToolCallData) ([]types.Message, error) {
	var history []types.Message
	var historyErr error
	if ae.memory != nil {
		history, historyErr = ae.memory.GetChatHistory(ctx)
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
	messages = append(messages, input.ToMessage("user"))

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
//   - ctx: context for cancellation
//   - messages: current round messages
//   - iteration: current iteration index
//
// Returns:
//   - execution result
//   - whether to continue iteration
//   - error information
func (ae *AgentEngine) executeIteration(ctx context.Context, messages []types.Message, iteration int) (*types.AgentResult, bool, error) {
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
	ae.mu.RUnlock()
	startTime := time.Now()
	logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

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

	response, err := ae.model.ChatWithTools(ctx, messages, tools)
	if err != nil {
		logger.LogError("executeIteration", err, slog.Int("iteration", iteration))
		return nil, false, errors.NewError(errors.EC_CHAT_FAILED.Code, "failed to chat with tools").Wrap(err)
	}

	result := &types.AgentResult{
		Output: response.Content,
		Usage:  response.Usage,
	}

	// Handle tool calls
	if len(response.ToolCalls) > 0 {
		logger.Info("LLM requested tool calls",
			slog.Int("tool_count", len(response.ToolCalls)),
			slog.Int("iteration", iteration+1))

		if iteration+1 >= maxIterations {
			logger.Info("Reached maximum iterations, skipping tool execution",
				slog.Int("iteration", iteration+1),
				slog.Int("max_iterations", maxIterations))
			return result, false, nil
		}

		// Sort tool calls by priority and dependencies
		sortedToolCalls, err := ae.sortToolCallsByDependencies(response.ToolCalls)
		if err != nil {
			logger.LogError("executeIteration", err, slog.String("phase", "sort_tool_calls"))
			// Continue with original order if sorting fails
			sortedToolCalls = response.ToolCalls
		}

		toolCalls := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
		intermediateSteps := make([]types.ToolCallData, 0, len(sortedToolCalls))

		for _, toolCall := range sortedToolCalls {
			logger.Info("Executing tool",
				slog.String("tool_name", toolCall.Function.Name),
				slog.Int("iteration", iteration+1))

			ae.mu.RLock()
			tool, exists := ae.toolsMap[toolCall.Function.Name]
			ae.mu.RUnlock()
			if !exists {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", toolCall.Function.Name)
				logger.Info("Tool not found",
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
				logger.LogToolExecution(toolCall.Function.Name, true, 0, slog.Bool("cached", true))
				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed (cached error): %v", toolCall.Function.Name, err)
					logger.LogToolExecution(toolCall.Function.Name, false, 0,
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
				toolResult, err = ae.executeToolWithTimeout(ctx, tool, toolCall.Function.Arguments, toolExecutionTimeout)
				duration := time.Since(toolStartTime)

				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed: %v", toolCall.Function.Name, err)
					logger.LogToolExecution(toolCall.Function.Name, false, duration,
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
				logger.LogToolExecution(toolCall.Function.Name, true, duration, slog.Bool("cached", false))
			}

			logger.Info("Tool executed successfully",
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
			observation := types.TruncateString(types.FormatToolResult(toolResult), truncationLength)

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
		logger.LogExecution("executeIteration", iteration,
			fmt.Sprintf("Iteration %d completed with %d tool calls", iteration+1, len(toolCalls)),
			slog.Int("tool_calls", len(toolCalls)),
			slog.Duration("duration", time.Since(startTime)))

		// If there are tool calls, usually need to continue iteration
		return result, len(toolCalls) > 0, nil
	}

	logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Iteration %d completed with no tool calls", iteration+1))
	return result, false, nil
}

// ==================== Message Building Methods ====================

// buildNextMessages builds messages for the next round
func (ae *AgentEngine) buildNextMessages(previousMessages []types.Message, result *types.AgentResult) []types.Message {
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
func (ae *AgentEngine) executeStreamWithIterations(ctx context.Context, initialMessages []types.Message, resultChan chan<- types.StreamResult) {
	messages := initialMessages
	finalResult := &types.AgentResult{}

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
		select {
		case <-ctx.Done():
			resultChan <- types.StreamResult{
				Type:  "error",
				Error: ctx.Err(),
			}
			return
		default:
		}

		iterationStartTime := time.Now()
		logger.LogExecution("executeStreamWithIterations", iteration,
			fmt.Sprintf("Starting streaming iteration %d/%d", iteration+1, maxIterations))

		// Execute single round iteration with streaming
		iterationResult, hasMore, err := ae.executeStreamIteration(ctx, messages, resultChan, iteration)
		if err != nil {
			logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1))
			resultChan <- types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_STREAM_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err),
			}
			return
		}

		// Accumulate final result
		finalResult.Output = iterationResult.Output
		finalResult.Usage.PromptTokens += iterationResult.Usage.PromptTokens
		finalResult.Usage.CompletionTokens += iterationResult.Usage.CompletionTokens
		finalResult.Usage.TotalTokens += iterationResult.Usage.TotalTokens
		toolCalls = append(toolCalls, iterationResult.ToolCalls...)
		intermediateSteps = append(intermediateSteps, iterationResult.IntermediateSteps...)

		// If no more tool calls, end iteration
		if !hasMore {
			logger.LogExecution("executeStreamWithIterations", iteration,
				"Streaming execution completed",
				slog.Int("total_iterations", iteration+1),
				slog.Duration("iteration_duration", time.Since(iterationStartTime)))
			break
		}

		if iteration+1 < maxIterations {
			logger.LogExecution("executeStreamWithIterations", iteration, "Preparing next iteration messages")
			messages = ae.buildNextMessages(messages, iterationResult)
		} else {
			logger.LogExecution("executeStreamWithIterations", iteration, "Reached maximum iterations")
		}
	}

	// Save to memory system
	if ae.memory != nil && len(initialMessages) > 0 {
		ae.mu.RLock()
		chatRole := ""
		if ae.config != nil {
			chatRole = ae.config.ChatMessageRole
		}
		ae.mu.RUnlock()

		if chatRole == "" {
			chatRole = "user"
		}
		lastMsg := initialMessages[len(initialMessages)-1]
		input := map[string]interface{}{
			"input": lastMsg.Content,
			"role":  chatRole,
			"parts": lastMsg.Parts,
		}
		output := map[string]interface{}{"output": finalResult.Output}
		if err := ae.memory.SaveContext(ctx, input, output); err != nil {
			logger.LogError("executeStreamWithIterations", err, slog.String("phase", "save_context"))
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
				history, err := ae.memory.GetChatHistory(ctx)
				if err == nil && len(history) > compressThreshold {
					ae.mu.RLock()
					llm := ae.model
					ae.mu.RUnlock()
					if llm != nil {
						// Execute compression asynchronously to avoid blocking the main thread
						go func() {
							defer func() {
								if r := recover(); r != nil {
									logger.LogError("executeStreamWithIterations", fmt.Errorf("panic in compress memory async: %v", r))
								}
							}()
							if err := ae.memory.CompressMemory(context.Background(), llm, compressThreshold); err != nil {
								logger.LogError("executeStreamWithIterations", err, slog.String("phase", "compress_memory_async"))
							} else {
								logger.Info("Memory compressed successfully",
									slog.Int("original_count", len(history)),
									slog.Int("threshold", compressThreshold))
							}
						}()
					}
				}
			}
		}
	}

	// Set final result's tool calls and intermediate steps
	finalResult.ToolCalls = toolCalls
	finalResult.IntermediateSteps = intermediateSteps

	// Update total usage
	ae.mu.Lock()
	ae.totalUsage.PromptTokens += finalResult.Usage.PromptTokens
	ae.totalUsage.CompletionTokens += finalResult.Usage.CompletionTokens
	ae.totalUsage.TotalTokens += finalResult.Usage.TotalTokens
	ae.mu.Unlock()

	logger.LogExecution("executeStreamWithIterations", 0, "Stream execution completed successfully",
		slog.Int("total_iterations", len(toolCalls)),
		slog.Int("total_tools", len(toolCalls)),
		slog.Int("total_tokens", finalResult.Usage.TotalTokens))

	resultChan <- types.StreamResult{
		Type:   "end",
		Result: finalResult,
	}
}

// executeStreamIteration executes a single streaming iteration
// Processes one round of streaming LLM calling and tool execution, supporting real-time content delivery
// Parameters:
//   - ctx: context for cancellation
//   - messages: current round messages
//   - resultChan: streaming result channel
//   - iteration: current iteration index
//
// Returns:
//   - execution result
//   - whether to continue iteration
//   - error information
func (ae *AgentEngine) executeStreamIteration(ctx context.Context, messages []types.Message, resultChan chan<- types.StreamResult, iteration int) (*types.AgentResult, bool, error) {
	result := &types.AgentResult{}

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

	// Add config values to context for LLM provider
	if ae.config != nil {
		if ae.config.Temperature > 0 {
			ctx = context.WithValue(ctx, types.ContextKeyTemperature, ae.config.Temperature)
		}
		if ae.config.MaxTokens > 0 {
			ctx = context.WithValue(ctx, types.ContextKeyMaxTokens, ae.config.MaxTokens)
		}
		if ae.config.MaxCompletionTokens > 0 {
			ctx = context.WithValue(ctx, types.ContextKeyMaxCompletionTokens, ae.config.MaxCompletionTokens)
		}
		if ae.config.TopP > 0 {
			ctx = context.WithValue(ctx, types.ContextKeyTopP, ae.config.TopP)
		}
	}

	if ae.model == nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "LLM model provider is nil")
	}

	stream, err := ae.model.ChatWithToolsStream(ctx, messages, tools)
	if err != nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "failed to chat with tools stream").Wrap(err)
	}

	intermediateSteps := []types.ToolCallData{}
	var outputBuilder strings.Builder
	outputBuilder.Grow(2048)

	for msg := range stream {
		if msg.Usage != nil {
			result.Usage = *msg.Usage
		}
		switch msg.Type {
		case "chunk":
			outputBuilder.WriteString(msg.Content)
			resultChan <- types.StreamResult{
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
		logger.LogExecution("executeStreamIteration", iteration, "Processing tool calls",
			slog.Int("tool_count", len(result.ToolCalls)))

		if iteration+1 >= maxIterations {
			logger.LogExecution("executeStreamIteration", iteration, "Reached maximum iterations, skipping tool execution")
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
			logger.LogError("executeStreamIteration", err, slog.String("phase", "sort_tool_calls"))
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
			logger.LogExecution("executeStreamIteration", iteration, "Executing tool",
				slog.String("tool_name", toolCall.Tool))

			ae.mu.RLock()
			tool, exists := ae.toolsMap[toolCall.Tool]
			ae.mu.RUnlock()
			if !exists {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", toolCall.Tool)
				logger.LogError("executeStreamIteration", fmt.Errorf("tool %q not found in available tools", toolCall.Tool),
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
				logger.LogToolExecution(toolCall.Tool, true, 0, slog.Bool("cached", true), slog.String("context", "streaming"))
				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed (cached error): %v", toolCall.Tool, err)
					logger.LogToolExecution(toolCall.Tool, false, 0,
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
				toolResult, err = ae.executeToolWithTimeout(ctx, tool, toolCall.ToolInput, toolExecutionTimeout)
				duration := time.Since(toolStartTime)

				if err != nil {
					errMsg := fmt.Sprintf("Tool '%s' execution failed: %v", toolCall.Tool, err)
					logger.LogToolExecution(toolCall.Tool, false, duration,
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
				logger.LogToolExecution(toolCall.Tool, true, duration, slog.Bool("cached", false), slog.String("context", "streaming"))
			}

			// Format observation from tool result
			truncationLength := ae.getToolTruncationLength(toolCall.Tool)
			observation := types.TruncateString(types.FormatToolResult(toolResult), truncationLength)

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

		logger.LogExecution("executeStreamIteration", iteration, "Tool execution completed",
			slog.Int("executed_tools", len(result.ToolCalls)),
			slog.Int("intermediate_steps", len(intermediateSteps)))

		return result, len(result.ToolCalls) > 0, nil
	}

	logger.LogExecution("executeStreamIteration", iteration, "No tool calls in this iteration")
	return result, false, nil
}
