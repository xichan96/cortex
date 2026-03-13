package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	stderrors "errors"

	"github.com/xichan96/cortex/agent/tools/schema"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
	"golang.org/x/sync/errgroup"
)

func (ae *AgentEngine) getConfig() *types.AgentConfig {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	return ae.config
}

func (ae *AgentEngine) getMaxIterations() int {
	if c := ae.getConfig(); c != nil {
		return c.MaxIterations
	}
	return 10
}

func (ae *AgentEngine) waitRateLimit(ctx context.Context) error {
	ae.mu.RLock()
	limiter := ae.rateLimiter
	ae.mu.RUnlock()
	if limiter == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := limiter.Wait(limitCtx); err != nil {
		return err
	}
	return nil
}

func toolCallData(tool string, toolInput interface{}, toolCallID, typeStr, observation string) types.ToolCallData {
	return types.ToolCallData{
		Action: types.ToolActionStep{
			Tool:       tool,
			ToolInput:  toolInput,
			ToolCallID: toolCallID,
			Type:       typeStr,
		},
		Observation: observation,
	}
}

func toolObservationError(err error, toolName string, cached bool, maxLen int) string {
	if maxLen <= 0 {
		maxLen = types.ToolErrorMaxLen
	}
	detail := types.NormalizeToolError(err, maxLen)
	suffix := ""
	if cached {
		suffix = " (cached error)"
	}
	if stderrors.Is(err, errors.EC_TOOL_INPUT_ERROR) {
		return fmt.Sprintf("Tool '%s' was called with invalid arguments%s: %s. Please fix the input to match the schema.", toolName, suffix, detail)
	}
	if stderrors.Is(err, errors.EC_TOOL_AUTH_ERROR) {
		return fmt.Sprintf("Tool '%s' authorization denied%s: %s", toolName, suffix, detail)
	}
	return fmt.Sprintf("Tool '%s' execution failed%s: %s", toolName, suffix, detail)
}

type stepResult struct {
	result   interface{}
	err      error
	cached   bool
	duration time.Duration
}

func (ae *AgentEngine) runToolCallsByLayer(ctx context.Context, sortedToolCalls []types.ToolCall, timeout time.Duration) (exists []bool, results []stepResult) {
	n := len(sortedToolCalls)
	tools := make([]types.Tool, n)
	exists = make([]bool, n)
	ae.mu.RLock()
	for i, tc := range sortedToolCalls {
		tools[i], exists[i] = ae.toolsMap[tc.Function.Name]
	}
	ae.mu.RUnlock()
	results = make([]stepResult, n)
	cfg := ae.getConfig()
	attempts := 1
	if cfg != nil && cfg.EnableToolRetry && cfg.RetryAttempts > 0 {
		attempts = cfg.RetryAttempts + 1
	}
	retryDelay := time.Second
	if cfg != nil && cfg.RetryDelay > 0 {
		retryDelay = cfg.RetryDelay
	}
	layers := ae.groupSortedToolCallsByLayer(sortedToolCalls)
	for _, layer := range layers {
		g, gctx := errgroup.WithContext(ctx)
		for _, idx := range layer {
			if !exists[idx] {
				continue
			}
			idx := idx
			tc := sortedToolCalls[idx]
			tool := tools[idx]
			g.Go(func() error {
				start := time.Now()
				// Validation errors are not retried; only execution failures go through the retry loop below.
				if err := schema.ValidateInput(tool.Schema(), tc.Function.Arguments, tool.Name()); err != nil {
					results[idx] = stepResult{err: err, cached: false, duration: 0}
					return nil
				}
				toolResult, err, cached := ae.getCachedToolResult(tc.Function.Name, tc.Function.Arguments)
				if cached {
					results[idx] = stepResult{result: toolResult, err: err, cached: true, duration: 0}
					return nil
				}
				var lastErr error
			retryLoop:
				for attempt := 0; attempt < attempts; attempt++ {
					select {
					case <-gctx.Done():
						lastErr = gctx.Err()
						break retryLoop
					default:
					}
					toolResult, err = ae.executeToolWithTimeout(gctx, tool, tc.Function.Arguments, timeout)
					if err == nil {
						results[idx] = stepResult{result: toolResult, cached: false, duration: time.Since(start)}
						ae.setCachedToolResult(tc.Function.Name, tc.Function.Arguments, toolResult, nil)
						return nil
					}
					lastErr = err
					if attempt < attempts-1 && errors.IsRetryable(err) {
						timer := time.NewTimer(retryDelay)
						defer timer.Stop()
						select {
						case <-gctx.Done():
							break retryLoop
						case <-timer.C:
						}
					} else {
						break retryLoop
					}
				}
				results[idx] = stepResult{err: lastErr, cached: false, duration: time.Since(start)}
				return nil
			})
		}
		_ = g.Wait()
	}
	return exists, results
}

func (ae *AgentEngine) checkDoomLoop(recentKeys []string, threshold int) (doom bool, lastKey string) {
	if threshold <= 0 || len(recentKeys) < threshold {
		return false, ""
	}
	last := recentKeys[len(recentKeys)-1]
	for i := len(recentKeys) - 1; i >= len(recentKeys)-threshold; i-- {
		if recentKeys[i] != last {
			return false, ""
		}
	}
	return true, last
}

func (ae *AgentEngine) enrichContextWithConfig(ctx context.Context) (context.Context, context.CancelFunc) {
	config := ae.getConfig()
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := time.Duration(0)
	if config != nil {
		timeout = config.Timeout
	}
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		return ae.addConfigValuesToContext(ctx), cancel
	}
	return ae.addConfigValuesToContext(ctx), func() {}
}

func (ae *AgentEngine) addConfigValuesToContext(ctx context.Context) context.Context {
	config := ae.getConfig()
	if config == nil {
		return ctx
	}
	if config.Temperature > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyTemperature, config.Temperature)
	}
	if config.MaxTokens > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyMaxTokens, config.MaxTokens)
	}
	if config.MaxCompletionTokens > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyMaxCompletionTokens, config.MaxCompletionTokens)
	}
	if config.TopP > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyTopP, config.TopP)
	}
	return ctx
}

func (ae *AgentEngine) saveToMemoryAndMaybeCompress(ctx context.Context, inputMap, outputMap map[string]interface{}) {
	if ae.memory == nil {
		return
	}
	if err := ae.memory.SaveContext(ctx, inputMap, outputMap); err != nil {
		logger.LogError("saveToMemoryAndMaybeCompress", err, slog.String("phase", "save_context"))
		return
	}
	config := ae.getConfig()
	enableCompress := false
	compressThreshold := 0
	if config != nil {
		enableCompress = config.EnableMemoryCompress
		compressThreshold = config.MemoryCompressThreshold
	}
	if !enableCompress || compressThreshold <= 0 {
		return
	}
	history, err := ae.memory.GetChatHistory(ctx)
	if err != nil || len(history) <= compressThreshold {
		return
	}
	ae.mu.RLock()
	llm := ae.model
	ae.mu.RUnlock()
	if llm == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError("saveToMemoryAndMaybeCompress", fmt.Errorf("panic in compress memory async: %v", r))
			}
		}()
		if err := ae.memory.CompressMemory(context.Background(), llm, compressThreshold); err != nil {
			logger.LogError("saveToMemoryAndMaybeCompress", err, slog.String("phase", "compress_memory_async"))
		} else {
			logger.Info("Memory compressed successfully",
				slog.Int("original_count", len(history)),
				slog.Int("threshold", compressThreshold))
		}
	}()
}

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

	if err := ae.waitRateLimit(ctx); err != nil {
		logger.LogError("Execute", err, slog.String("phase", "rate_limit"))
		return nil, errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err)
	}

	messages, err := ae.prepareMessages(ctx, input, previousRequests)
	if err != nil {
		logger.LogError("Execute", err, slog.String("phase", "prepare_messages"))
		return nil, errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, errors.EC_PREPARE_MESSAGES_FAILED.Message).Wrap(err)
	}

	var finalResult *types.AgentResult
	iteration := 0
	maxIterations := ae.getMaxIterations()
	finalResult = &types.AgentResult{Output: ""}
	totalUsage := types.Usage{}
	config := ae.getConfig()
	doomThreshold := 0
	var onDoomLoop func(string, map[string]interface{}) bool
	if config != nil {
		doomThreshold = config.DoomLoopThreshold
		onDoomLoop = config.OnDoomLoop
	}
	var recentKeys []string
	const maxRecentKeys = 20
	for iteration < maxIterations {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		logger.LogExecution("Execute", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

		result, continueIterating, err := ae.executeIteration(ctx, messages, iteration)
		if err != nil {
			logger.LogError("Execute", err, slog.Int("iteration", iteration+1))
			return nil, errors.NewError(errors.EC_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err)
		}

		totalUsage.PromptTokens += result.Usage.PromptTokens
		totalUsage.CompletionTokens += result.Usage.CompletionTokens
		totalUsage.TotalTokens += result.Usage.TotalTokens
		finalResult = result
		finalResult.Usage = totalUsage

		if !continueIterating || len(result.ToolCalls) == 0 {
			logger.LogExecution("Execute", iteration, "Execution completed, no more tool calls")
			break
		}

		for _, tc := range result.ToolCalls {
			recentKeys = append(recentKeys, generateToolCacheKey(tc.Tool, tc.ToolInput))
		}
		if len(recentKeys) > maxRecentKeys {
			recentKeys = recentKeys[len(recentKeys)-maxRecentKeys:]
		}
		if doom, lastKey := ae.checkDoomLoop(recentKeys, doomThreshold); doom && onDoomLoop != nil {
			toolName := lastKey
			if idx := strings.Index(lastKey, ":"); idx > 0 {
				toolName = lastKey[:idx]
			}
			var lastInput map[string]interface{}
			for _, tc := range result.ToolCalls {
				if tc.Tool == toolName {
					lastInput = tc.ToolInput
					break
				}
			}
			if !onDoomLoop(toolName, lastInput) {
				logger.LogExecution("Execute", iteration, "Doom loop detected, stopping by callback")
				break
			}
		}

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

	if ae.memory != nil && finalResult != nil {
		chatRole := "user"
		if c := ae.getConfig(); c != nil && c.ChatMessageRole != "" {
			chatRole = c.ChatMessageRole
		}
		ae.saveToMemoryAndMaybeCompress(ctx, map[string]interface{}{
			"input": input.String(), "role": chatRole, "parts": input.Parts,
		}, map[string]interface{}{"output": finalResult.Output})
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

		if err := ae.waitRateLimit(ctx); err != nil {
			logger.LogError("ExecuteStream", err, slog.String("phase", "rate_limit"))
			resultChan <- types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err),
			}
			return
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

	config := ae.getConfig()
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
	tools := ae.tools
	toolExecutionTimeout := time.Duration(0)
	if ae.config != nil {
		toolExecutionTimeout = ae.config.ToolExecutionTimeout
	}
	ae.mu.RUnlock()
	maxIterations := ae.getMaxIterations()
	ctx, cancel := ae.enrichContextWithConfig(ctx)
	defer cancel()
	startTime := time.Now()
	logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

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

		sortedToolCalls, err := ae.sortToolCallsByDependencies(response.ToolCalls)
		if err != nil {
			logger.LogError("executeIteration", err, slog.String("phase", "sort_tool_calls"))
			sortedToolCalls = response.ToolCalls
		}
		cfg := ae.getConfig()
		maxPerIter := 0
		if cfg != nil && cfg.MaxToolCallsPerIteration > 0 {
			maxPerIter = cfg.MaxToolCallsPerIteration
		}
		if maxPerIter > 0 && len(sortedToolCalls) > maxPerIter {
			logger.Info("Capping tool calls per iteration", slog.Int("requested", len(sortedToolCalls)), slog.Int("cap", maxPerIter))
			sortedToolCalls = sortedToolCalls[:maxPerIter]
		}

		toolCalls := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
		intermediateSteps := make([]types.ToolCallData, 0, len(sortedToolCalls))
		exists, results := ae.runToolCallsByLayer(ctx, sortedToolCalls, toolExecutionTimeout)
		writeDir := ""
		if cfg != nil {
			writeDir = cfg.ToolResultWriteDir
		}
		for i, toolCall := range sortedToolCalls {
			name := toolCall.Function.Name
			args := toolCall.Function.Arguments
			logger.Info("Executing tool", slog.String("tool_name", name), slog.Int("iteration", iteration+1))
			if !exists[i] {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", name)
				logger.Info("Tool not found", slog.String("tool_name", name), slog.Int("iteration", iteration+1))
				intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, errMsg))
				continue
			}
			r := results[i]
			if r.err != nil {
				if r.cached {
					logger.LogToolExecution(name, true, 0, slog.Bool("cached", true))
				}
				errMsg := toolObservationError(r.err, name, r.cached, ae.getToolErrorMaxLen())
				logger.LogToolExecution(name, false, r.duration, slog.String("error", r.err.Error()), slog.Bool("cached", r.cached))
				intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, errMsg))
				continue
			}
			if r.cached {
				logger.LogToolExecution(name, true, 0, slog.Bool("cached", true))
			} else {
				logger.LogToolExecution(name, true, r.duration, slog.Bool("cached", false))
			}
			logger.Info("Tool executed successfully", slog.String("tool_name", name), slog.Int("iteration", iteration+1))
			toolCalls = append(toolCalls, types.ToolCallRequest{
				Tool: name, ToolInput: args, ToolCallID: toolCall.ID, Type: toolCall.Type,
			})
			truncationLength := ae.getToolTruncationLength(name)
			sanitized := types.SanitizeToolResult(r.result, truncationLength)
			formatted := types.FormatToolResult(sanitized)
			observation, _, _ := types.TruncateToolResult(formatted, truncationLength, writeDir)
			intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, observation))
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

	// Keep assistant's previous response if it has content or tool calls
	// Assistant message with tool_calls MUST precede tool messages (OpenAI API requirement)
	if result != nil && (result.Output != "" || len(result.ToolCalls) > 0) {
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

	// Add tool call results to messages
	if result != nil && len(result.ToolCalls) > 0 {
		for i, r := range result.ToolCalls {
			toolResultMessage := types.Message{
				Role:       "tool",
				Content:    result.IntermediateSteps[i].Observation,
				ToolCallID: r.ToolCallID,
			}
			messages = append(messages, toolResultMessage)
		}
	}

	return messages
}

// ==================== Streaming Execution Methods ====================

// executeStreamWithIterations executes streaming iterations (supports multi-round tool calling)
func (ae *AgentEngine) executeStreamWithIterations(ctx context.Context, initialMessages []types.Message, resultChan chan<- types.StreamResult) {
	messages := initialMessages
	finalResult := &types.AgentResult{}
	maxIterations := ae.getMaxIterations()
	estimatedToolCalls := maxIterations * 3
	toolCalls := make([]types.ToolCallRequest, 0, estimatedToolCalls)
	intermediateSteps := make([]types.ToolCallData, 0, estimatedToolCalls)
	config := ae.getConfig()
	doomThreshold := 0
	var onDoomLoop func(string, map[string]interface{}) bool
	if config != nil {
		doomThreshold = config.DoomLoopThreshold
		onDoomLoop = config.OnDoomLoop
	}
	var recentKeys []string
	const maxRecentKeys = 20
	var iteration int

	for iteration = 0; iteration < maxIterations; iteration++ {
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

		iterationResult, hasMore, err := ae.executeStreamIteration(ctx, messages, resultChan, iteration)
		if err != nil {
			logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1))
			resultChan <- types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_STREAM_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err),
			}
			return
		}

		finalResult.Output = iterationResult.Output
		finalResult.Usage.PromptTokens += iterationResult.Usage.PromptTokens
		finalResult.Usage.CompletionTokens += iterationResult.Usage.CompletionTokens
		finalResult.Usage.TotalTokens += iterationResult.Usage.TotalTokens
		toolCalls = append(toolCalls, iterationResult.ToolCalls...)
		intermediateSteps = append(intermediateSteps, iterationResult.IntermediateSteps...)

		if !hasMore {
			logger.LogExecution("executeStreamWithIterations", iteration,
				"Streaming execution completed",
				slog.Int("total_iterations", iteration+1),
				slog.Duration("iteration_duration", time.Since(iterationStartTime)))
			break
		}

		for _, tc := range iterationResult.ToolCalls {
			recentKeys = append(recentKeys, generateToolCacheKey(tc.Tool, tc.ToolInput))
		}
		if len(recentKeys) > maxRecentKeys {
			recentKeys = recentKeys[len(recentKeys)-maxRecentKeys:]
		}
		if doom, lastKey := ae.checkDoomLoop(recentKeys, doomThreshold); doom && onDoomLoop != nil {
			toolName := lastKey
			if idx := strings.Index(lastKey, ":"); idx > 0 {
				toolName = lastKey[:idx]
			}
			var lastInput map[string]interface{}
			for _, tc := range iterationResult.ToolCalls {
				if tc.Tool == toolName {
					lastInput = tc.ToolInput
					break
				}
			}
			if !onDoomLoop(toolName, lastInput) {
				logger.LogExecution("executeStreamWithIterations", iteration, "Doom loop detected, stopping by callback")
				break
			}
		}

		if iteration+1 < maxIterations {
			logger.LogExecution("executeStreamWithIterations", iteration, "Preparing next iteration messages")
			messages = ae.buildNextMessages(messages, iterationResult)
		} else {
			logger.LogExecution("executeStreamWithIterations", iteration, "Reached maximum iterations")
		}
	}

	if ae.memory != nil && len(initialMessages) > 0 {
		chatRole := "user"
		if c := ae.getConfig(); c != nil && c.ChatMessageRole != "" {
			chatRole = c.ChatMessageRole
		}
		lastMsg := initialMessages[len(initialMessages)-1]
		ae.saveToMemoryAndMaybeCompress(ctx, map[string]interface{}{
			"input": lastMsg.Content, "role": chatRole, "parts": lastMsg.Parts,
		}, map[string]interface{}{"output": finalResult.Output})
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
		slog.Int("total_iterations", iteration+1),
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
	toolExecutionTimeout := time.Duration(0)
	if ae.config != nil {
		toolExecutionTimeout = ae.config.ToolExecutionTimeout
	}
	ae.mu.RUnlock()
	maxIterations := ae.getMaxIterations()
	ctx, cancel := ae.enrichContextWithConfig(ctx)
	defer cancel()

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

		sortedToolCalls, err := ae.sortToolCallsByDependencies(toolCallsForSorting)
		if err != nil {
			logger.LogError("executeStreamIteration", err, slog.String("phase", "sort_tool_calls"))
			sortedToolCalls = toolCallsForSorting
		}
		cfg := ae.getConfig()
		maxPerIter := 0
		if cfg != nil && cfg.MaxToolCallsPerIteration > 0 {
			maxPerIter = cfg.MaxToolCallsPerIteration
		}
		if maxPerIter > 0 && len(sortedToolCalls) > maxPerIter {
			logger.Info("Capping tool calls per iteration", slog.Int("requested", len(sortedToolCalls)), slog.Int("cap", maxPerIter), slog.String("context", "streaming"))
			sortedToolCalls = sortedToolCalls[:maxPerIter]
		}

		streamToolCalls := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
		existsStream, resultsStream := ae.runToolCallsByLayer(ctx, sortedToolCalls, toolExecutionTimeout)
		writeDir := ""
		if cfg != nil {
			writeDir = cfg.ToolResultWriteDir
		}
		for i, tc := range sortedToolCalls {
			name := tc.Function.Name
			args := tc.Function.Arguments
			logger.LogExecution("executeStreamIteration", iteration, "Executing tool", slog.String("tool_name", name))
			if !existsStream[i] {
				errMsg := fmt.Sprintf("tool '%s' not found in available tools", name)
				logger.LogError("executeStreamIteration", fmt.Errorf("tool %q not found in available tools", name), slog.String("tool_name", name))
				intermediateSteps = append(intermediateSteps, toolCallData(name, args, tc.ID, tc.Type, errMsg))
				continue
			}
			r := resultsStream[i]
			if r.err != nil {
				if r.cached {
					logger.LogToolExecution(name, true, 0, slog.Bool("cached", true), slog.String("context", "streaming"))
				}
				errMsg := toolObservationError(r.err, name, r.cached, ae.getToolErrorMaxLen())
				logger.LogToolExecution(name, false, r.duration, slog.String("error", r.err.Error()), slog.Bool("cached", r.cached), slog.String("context", "streaming"))
				intermediateSteps = append(intermediateSteps, toolCallData(name, args, tc.ID, tc.Type, errMsg))
				continue
			}
			if r.cached {
				logger.LogToolExecution(name, true, 0, slog.Bool("cached", true), slog.String("context", "streaming"))
			} else {
				logger.LogToolExecution(name, true, r.duration, slog.Bool("cached", false), slog.String("context", "streaming"))
			}
			streamToolCalls = append(streamToolCalls, types.ToolCallRequest{
				Tool: name, ToolInput: args, ToolCallID: tc.ID, Type: tc.Type,
			})
			truncationLength := ae.getToolTruncationLength(name)
			sanitized := types.SanitizeToolResult(r.result, truncationLength)
			formatted := types.FormatToolResult(sanitized)
			observation, _, _ := types.TruncateToolResult(formatted, truncationLength, writeDir)
			intermediateSteps = append(intermediateSteps, toolCallData(name, args, tc.ID, tc.Type, observation))
		}

		result.ToolCalls = streamToolCalls
		result.IntermediateSteps = intermediateSteps

		logger.LogExecution("executeStreamIteration", iteration, "Tool execution completed",
			slog.Int("executed_tools", len(streamToolCalls)),
			slog.Int("intermediate_steps", len(intermediateSteps)))

		return result, len(streamToolCalls) > 0, nil
	}

	logger.LogExecution("executeStreamIteration", iteration, "No tool calls in this iteration")
	return result, false, nil
}
