package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	stderrors "errors"

	"github.com/xichan96/cortex/agent/hooks"
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

func (ae *AgentEngine) getHooks() hooks.Hooks {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	if ae.hooks != nil {
		return ae.hooks
	}
	return hooks.NoOpHooks{}
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

// sendStreamResult sends a result to the stream channel, unblocking when the
// caller's turn ctx is cancelled so a stuck consumer (stopped range / slow UI)
// cannot deadlock the engine. It reports whether the result was actually sent.
// The channel is never closed before this returns, so a blocking send is safe.
// A nil ctx is treated as a background ctx (no cancellation guard).
func sendStreamResult(ctx context.Context, ch chan<- types.StreamResult, result types.StreamResult) bool {
	// Terminal error results must never be dropped: the consumer relies on them
	// to learn about cancellation/failure (e.g. subagents folding ctx cancel
	// into a "cancelled" status). With a cancelled ctx the select below would
	// randomly drop the send, so force-deliver error results.
	if result.Error != nil {
		ch <- result
		return true
	}
	if ctx == nil {
		ch <- result
		return true
	}
	select {
	case ch <- result:
		return true
	case <-ctx.Done():
		return false
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
		return fmt.Sprintf("Tool '%s' was called with invalid arguments%s: %s. Please rewrite the input so it satisfies the expected schema.", toolName, suffix, detail)
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

// streamToolCallback wraps ToolCallback to send events to stream result channel
type streamToolCallback struct {
	userCallback  types.ToolCallback
	resultSender  func(types.StreamResult)
	mu            sync.Mutex
	toolCallState map[string]time.Time // toolCallID -> startTime
	closed        bool
}

func newStreamToolCallback(userCallback types.ToolCallback, sender func(types.StreamResult)) *streamToolCallback {
	return &streamToolCallback{
		userCallback:  userCallback,
		resultSender:  sender,
		toolCallState: make(map[string]time.Time),
		closed:        false,
	}
}

func (c *streamToolCallback) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.toolCallState = make(map[string]time.Time)
}

func (c *streamToolCallback) sendEvent(event *types.ToolEvent) {
	if c.closed || c.resultSender == nil {
		return
	}
	c.resultSender(types.StreamResult{
		Type:      "tool_event",
		ToolEvent: event,
	})
}

func (c *streamToolCallback) OnToolCall(toolName string, toolCallID string, input map[string]interface{}) {
	c.sendEvent(&types.ToolEvent{
		Event:      types.StreamEventToolCall,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		State:      types.ToolStatePending,
		Input:      input,
	})
	if c.userCallback != nil {
		c.userCallback.OnToolCall(toolName, toolCallID, input)
	}
}

func (c *streamToolCallback) OnToolInputStart(toolName string, toolCallID string, input map[string]interface{}) {
	c.mu.Lock()
	c.toolCallState[toolCallID] = time.Now()
	c.mu.Unlock()

	c.sendEvent(&types.ToolEvent{
		Event:      types.StreamEventToolInputStart,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		State:      types.ToolStateRunning,
		Input:      input,
	})
	if c.userCallback != nil {
		c.userCallback.OnToolInputStart(toolName, toolCallID, input)
	}
}

func (c *streamToolCallback) OnToolInputEnd(toolName string, toolCallID string, input map[string]interface{}) {
	c.sendEvent(&types.ToolEvent{
		Event:      types.StreamEventToolInputEnd,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		State:      types.ToolStateRunning,
		Input:      input,
	})
	if c.userCallback != nil {
		c.userCallback.OnToolInputEnd(toolName, toolCallID, input)
	}
}

func (c *streamToolCallback) OnToolResult(toolName string, toolCallID string, output interface{}) {
	var duration time.Duration
	c.mu.Lock()
	if start, ok := c.toolCallState[toolCallID]; ok {
		duration = time.Since(start)
		delete(c.toolCallState, toolCallID)
	}
	c.mu.Unlock()

	c.sendEvent(&types.ToolEvent{
		Event:      types.StreamEventToolResult,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		State:      types.ToolStateCompleted,
		Output:     output,
		Duration:   duration,
	})
	if c.userCallback != nil {
		c.userCallback.OnToolResult(toolName, toolCallID, output)
	}
}

func (c *streamToolCallback) OnToolError(toolName string, toolCallID string, err error) {
	var duration time.Duration
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	c.mu.Lock()
	if start, ok := c.toolCallState[toolCallID]; ok {
		duration = time.Since(start)
		delete(c.toolCallState, toolCallID)
	}
	c.mu.Unlock()

	c.sendEvent(&types.ToolEvent{
		Event:      types.StreamEventToolError,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		State:      types.ToolStateError,
		Error:      errMsg,
		Duration:   duration,
	})
	if c.userCallback != nil {
		c.userCallback.OnToolError(toolName, toolCallID, err)
	}
}

func (ae *AgentEngine) prepareToolCalls(toolCalls []types.ToolCall) ([]types.ToolCall, error) {
	if len(toolCalls) <= 1 {
		return toolCalls, nil
	}
	sorted, err := ae.sortToolCallsByDependencies(toolCalls)
	if err != nil {
		logger.LogError("prepareToolCalls", err, slog.String("phase", "sort_tool_calls"))
		sorted = toolCalls
	}
	cfg := ae.getConfig()
	if cfg != nil && cfg.MaxToolCallsPerIteration > 0 && len(sorted) > cfg.MaxToolCallsPerIteration {
		logger.Info("Capping tool calls per iteration", slog.Int("requested", len(sorted)), slog.Int("cap", cfg.MaxToolCallsPerIteration))
		sorted = sorted[:cfg.MaxToolCallsPerIteration]
	}
	return sorted, nil
}

func (ae *AgentEngine) buildToolCallResults(sortedToolCalls []types.ToolCall, exists []bool, results []stepResult, writeDir, logContext string) ([]types.ToolCallRequest, []types.ToolCallData) {
	toolCalls := make([]types.ToolCallRequest, 0, len(sortedToolCalls))
	intermediateSteps := make([]types.ToolCallData, 0, len(sortedToolCalls))
	for i, toolCall := range sortedToolCalls {
		name := toolCall.Function.Name
		args := toolCall.Function.Arguments
		if !exists[i] {
			errMsg := fmt.Sprintf("tool '%s' not found in available tools", name)
			intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, errMsg))
			continue
		}
		r := results[i]
		if r.err != nil {
			if r.cached {
				logger.LogToolExecution(name, true, 0, slog.Bool("cached", true), slog.String("context", logContext))
			}
			errMsg := toolObservationError(r.err, name, r.cached, ae.getToolErrorMaxLen())
			logger.LogToolExecution(name, false, r.duration, slog.String("error", r.err.Error()), slog.Bool("cached", r.cached), slog.String("context", logContext))
			intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, errMsg))
			continue
		}
		if r.cached {
			logger.LogToolExecution(name, true, 0, slog.Bool("cached", true), slog.String("context", logContext))
		} else {
			logger.LogToolExecution(name, true, r.duration, slog.Bool("cached", false), slog.String("context", logContext))
		}
		toolCalls = append(toolCalls, types.ToolCallRequest{
			Tool: name, ToolInput: args, ToolCallID: toolCall.ID, Type: toolCall.Type,
		})
		truncationLength := ae.getToolTruncationLength(name)
		sanitized := types.SanitizeToolResult(r.result, truncationLength)
		formatted := types.FormatToolResult(sanitized)
		header := ae.buildOutputHeader(toolCall, name, r, formatted)
		observation, _ := types.TruncateToolResult(formatted, truncationLength, writeDir, header)
		intermediateSteps = append(intermediateSteps, toolCallData(name, args, toolCall.ID, toolCall.Type, observation))
	}
	return toolCalls, intermediateSteps
}

// buildOutputHeader assembles the structured header for a tool observation.
// ExitCode is probed from bash/command-style results (map with exit_code).
func (ae *AgentEngine) buildOutputHeader(toolCall types.ToolCall, name string, r stepResult, formatted string) types.OutputHeader {
	h := types.OutputHeader{
		ChunkID:        toolCall.ID,
		WallTime:       r.duration,
		OriginalBytes:  len(formatted),
		OriginalTokens: types.RoughTokenEstimate(formatted),
	}
	if lines := strings.Count(formatted, "\n"); lines > 0 {
		h.TotalLines = lines + 1
	}
	if m, ok := r.result.(map[string]interface{}); ok {
		if ec, ok := m["exit_code"]; ok {
			if code, ok := ec.(int); ok {
				h.ExitCode = &code
			}
		}
	}
	return h
}

func (ae *AgentEngine) getToolByName(name string) (types.Tool, bool) {
	ae.mu.RLock()
	defer ae.mu.RUnlock()
	if t, ok := ae.toolsMap[name]; ok {
		return t, true
	}
	for k, t := range ae.toolsMap {
		if strings.EqualFold(k, name) {
			return t, true
		}
	}
	return nil, false
}

func (ae *AgentEngine) runToolCallsByLayer(ctx context.Context, sortedToolCalls []types.ToolCall, timeout time.Duration) (exists []bool, results []stepResult) {
	n := len(sortedToolCalls)
	tools := make([]types.Tool, n)
	exists = make([]bool, n)
	for i, tc := range sortedToolCalls {
		tools[i], exists[i] = ae.getToolByName(tc.Function.Name)
	}
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

	// Get callback for tool events
	ae.mu.RLock()
	callback := ae.toolCallback
	ae.mu.RUnlock()

	// Get hooks
	hookRunner := hooks.NewRunner(ae.getHooks(), "", "")

	// Global concurrency cap across all layers: parallel bash/MCP calls (same
	// layer, no dependencies) are bounded, dependent calls in later layers wait
	// for earlier layers to release their slots. This is a wall-time throttle
	// only — correctness is unchanged (errgroup already waits for every call).
	limit := ae.getToolParallelismLimit()
	for _, layer := range layers {
		g, gctx := errgroup.WithContext(ctx)
		if limit > 0 {
			g.SetLimit(limit)
		}
		for _, idx := range layer {
			if !exists[idx] {
				continue
			}
			idx := idx
			tc := sortedToolCalls[idx]
			tool := tools[idx]
			toolCallID := tc.ID
			args := tc.Function.Arguments
			if args == nil {
				args = make(map[string]interface{})
			}
			g.Go(func() error {
				start := time.Now()

				// Emit tool call event and call OnBeforeToolCall hook
				if callback != nil {
					callback.OnToolCall(tool.Name(), toolCallID, args)
					callback.OnToolInputStart(tool.Name(), toolCallID, args)
				}
				hookRunner.BeforeToolCall(tool.Name(), args)

				if err := schema.ValidateInput(tool.Schema(), args, tool.Name()); err != nil {
					// Input validation failure is fatal (F3): retrying the same
					// arguments cannot succeed, and running the other parallel
					// calls with the model's broken inputs is wasted work. Mark
					// it fatal and cancel the same-layer siblings via the
					// errgroup so this iteration unwinds quickly.
					results[idx] = stepResult{err: &types.FatalToolError{Err: err, Reason: "tool input validation failed"}, cached: false, duration: 0}
					if callback != nil {
						callback.OnToolInputEnd(tool.Name(), toolCallID, args)
						callback.OnToolError(tool.Name(), toolCallID, err)
					}
					return err
				}
				canonicalName := tool.Name()
				// Check for no_cache flag in metadata
				noCache := false
				if meta := tool.Metadata(); meta.Extra != nil {
					if v, ok := meta.Extra["no_cache"].(bool); ok && v {
						noCache = true
					}
				}

				var toolResult interface{}
				var err error
				var cached bool

				if !noCache {
					toolResult, err, cached = ae.getCachedToolResult(canonicalName, args)
					if cached {
						results[idx] = stepResult{result: toolResult, err: err, cached: true, duration: 0}
						if callback != nil {
							callback.OnToolInputEnd(tool.Name(), toolCallID, args)
							callback.OnToolResult(tool.Name(), toolCallID, toolResult)
						}
						return nil
					}
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
					// Emit running state
					if callback != nil && attempt == 0 {
						callback.OnToolInputEnd(tool.Name(), toolCallID, args)
					}
					toolTimeout := ae.getToolTimeout(tool.Name(), args)
					toolResult, err = ae.executeToolWithTimeout(gctx, tool, args, toolTimeout)
					if err == nil {
						results[idx] = stepResult{result: toolResult, cached: false, duration: time.Since(start)}
						if !noCache {
							ae.setCachedToolResult(canonicalName, args, toolResult, nil)
						}
						if callback != nil {
							callback.OnToolResult(tool.Name(), toolCallID, toolResult)
						}
						hookRunner.AfterToolCall(tool.Name(), toolResult, nil)
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
				if callback != nil {
					callback.OnToolError(tool.Name(), toolCallID, lastErr)
				}
				hookRunner.AfterToolCall(tool.Name(), nil, lastErr)
				return nil
			})
		}
		_ = g.Wait()
	}
	return exists, results
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
	if n := config.EffectiveMaxCompletionTokens(); n > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyMaxCompletionTokens, n)
	}
	if config.TopP > 0 {
		ctx = context.WithValue(ctx, types.ContextKeyTopP, config.TopP)
	}
	return ctx
}

func memoryOutputMap(result *types.AgentResult, allIntermediate []types.ToolCallData) map[string]interface{} {
	if result == nil {
		return map[string]interface{}{"output": ""}
	}
	m := map[string]interface{}{"output": result.Output}
	steps := result.IntermediateSteps
	if len(allIntermediate) > 0 {
		steps = allIntermediate
	}
	if len(steps) > 0 {
		m["intermediate_steps"] = steps
	}
	return m
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
	if config == nil || !config.EnableMemoryCompress {
		return
	}
	compressThreshold := config.MemoryCompressThreshold
	compactTurns := config.CompactAfterTurns
	if compressThreshold <= 0 && compactTurns <= 0 {
		return
	}

	byTurn := false
	if compactTurns > 0 {
		n := ae.memoryCompletedTurns.Add(1)
		if int(n) >= compactTurns {
			byTurn = true
			ae.memoryCompletedTurns.Store(0)
		}
	}

	stored := 0
	if ctr, ok := ae.memory.(interface {
		StoredMessageCount(context.Context) (int, error)
	}); ok {
		n, err := ctr.StoredMessageCount(ctx)
		if err != nil {
			logger.LogError("saveToMemoryAndMaybeCompress", err, slog.String("phase", "stored_message_count"))
			return
		}
		stored = n
	} else {
		history, err := ae.memory.GetChatHistory(ctx)
		if err != nil {
			logger.LogError("saveToMemoryAndMaybeCompress", err, slog.String("phase", "chat_history_gate"))
			return
		}
		stored = len(history)
	}

	byCount := compressThreshold > 0 && stored > compressThreshold
	if !byTurn && !byCount {
		return
	}

	keepWindow := compressThreshold
	if keepWindow <= 0 {
		keepWindow = config.MaxHistoryMessages
	}
	if keepWindow <= 0 {
		keepWindow = 50
	}

	ae.mu.RLock()
	llm := ae.model
	ae.mu.RUnlock()
	if llm == nil {
		return
	}
	reason := "threshold"
	if byTurn && !byCount {
		reason = "compact_after_turns"
	}
	if byTurn && byCount {
		reason = "threshold_and_turns"
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError("saveToMemoryAndMaybeCompress", fmt.Errorf("panic in compress memory async: %v", r))
			}
		}()
		ae.memoryCompressMu.Lock()
		defer ae.memoryCompressMu.Unlock()
		dt := config.Timeout
		if dt <= 0 {
			dt = 10 * time.Minute
		} else if dt < 2*time.Minute {
			dt = 2 * time.Minute
		}
		compressCtx, cancel := context.WithTimeout(context.Background(), dt)
		defer cancel()
		if err := ae.memory.CompressMemory(compressCtx, llm, keepWindow); err != nil {
			logger.LogError("saveToMemoryAndMaybeCompress", err, slog.String("phase", "compress_memory_async"))
		} else {
			logger.Info("Memory compressed successfully",
				slog.String("reason", reason),
				slog.Int("stored_message_count", stored),
				slog.Int("keep_window", keepWindow))
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
	var doomState doomLoopState
	var allIntermediateSteps []types.ToolCallData
	var hitDoomBlock bool
	var maxIterStop bool
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
		totalUsage.CachedTokens += result.Usage.CachedTokens
		totalUsage.CacheCreationTokens += result.Usage.CacheCreationTokens
		finalResult = result
		finalResult.Usage = totalUsage
		allIntermediateSteps = append(allIntermediateSteps, result.IntermediateSteps...)

		if !continueIterating || len(result.IntermediateSteps) == 0 {
			logger.LogExecution("Execute", iteration, "Execution completed, no more tool calls")
			break
		}

		doomState.appendSteps(result.IntermediateSteps)
		if doomState.shouldStop(doomThreshold, onDoomLoop) {
			logger.LogExecution("Execute", iteration, "Doom loop detected, stopping")
			hitDoomBlock = true
			break
		}

		messages = ae.buildNextMessages(messages, result)
		if iteration+1 >= maxIterations {
			maxIterStop = true
		}
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

	if finalResult != nil {
		switch {
		case hitDoomBlock:
			finalResult.StopCause = types.AgentStopCauseDoomLoop
		case maxIterStop:
			finalResult.StopCause = types.AgentStopCauseMaxIterations
		}
	}

	if ae.memory != nil && finalResult != nil {
		chatRole := "user"
		if c := ae.getConfig(); c != nil && c.ChatMessageRole != "" {
			chatRole = c.ChatMessageRole
		}
		ae.saveToMemoryAndMaybeCompress(ctx, map[string]interface{}{
			"input": input.String(), "role": chatRole, "parts": input.Parts,
		}, memoryOutputMap(finalResult, allIntermediateSteps))
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

	resultChan := make(chan types.StreamResult, ae.getStreamBufferSize())

	// Set result sender for tool events. It must honor the caller's turn ctx
	// (NOT ae.ctx, which is the engine-lifetime ctx and is never cancelled by
	// ExecuteStream): when the channel is full the send blocks so tool events
	// are never silently dropped, and it unblocks when the turn is cancelled so
	// a stuck consumer (stopped range / slow UI) cannot deadlock the engine.
	turnCtx := ctx
	ae.mu.Lock()
	ae.resultSender = func(result types.StreamResult) {
		select {
		case resultChan <- result:
		case <-turnCtx.Done():
		case <-ae.ctx.Done():
		}
	}
	ae.mu.Unlock()

	// Create wrapped tool callback that sends events to stream
	userCallback := ae.toolCallback
	var wrappedCallback *streamToolCallback
	if userCallback != nil || ae.resultSender != nil {
		wrappedCallback = newStreamToolCallback(userCallback, ae.resultSender)
		ae.mu.Lock()
		ae.toolCallback = wrappedCallback
		ae.mu.Unlock()
	}

	go func() {
		// Create hooks runner
		ae.mu.RLock()
		hookRunner := hooks.NewRunner(ae.hooks, "", "")
		ae.mu.RUnlock()

		defer func() {
			// Clear result sender and restore original callback
			ae.mu.Lock()
			ae.resultSender = nil
			if userCallback != nil {
				ae.toolCallback = userCallback
			} else {
				ae.toolCallback = nil
			}
			ae.mu.Unlock()

			// Close stream callback to clean up resources
			if wrappedCallback != nil {
				wrappedCallback.Close()
			}

			close(resultChan)
			ae.isRunning.Store(false)
		}()

		startTime := time.Now()
		logger.LogExecution("ExecuteStream", 0, "Starting stream execution", slog.String("input", types.TruncateString(input.String(), 100)), slog.Int("previousRequests", len(previousRequests)))

		// Call OnBeforeStart hook
		if err := hookRunner.BeforeStart(&input); err != nil {
			logger.LogError("ExecuteStream", err, slog.String("phase", "on_before_start"))
			sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_HOOK_FAILED.Code, "hook OnBeforeStart failed").Wrap(err),
			})
			return
		}

		if err := ae.waitRateLimit(ctx); err != nil {
			logger.LogError("ExecuteStream", err, slog.String("phase", "rate_limit"))
			sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:  "error",
				Error: errors.NewError(errors.EC_SYSTEM_OVERLOAD.Code, "rate limit exceeded").Wrap(err),
			})
			return
		}

		defer func() {
			if r := recover(); r != nil {
				logger.LogError("ExecuteStream", fmt.Errorf("panic recovered: %v", r))
				hookRunner.OnError(fmt.Errorf("panic: %v", r))
				sendStreamResult(ctx, resultChan, types.StreamResult{
					Type:  "error",
					Error: errors.NewError(errors.EC_STREAM_PANIC.Code, "panic in stream execution").Wrap(fmt.Errorf("%v", r)),
				})
			}
		}()

		// Prepare initial messages
		messages, err := ae.prepareMessages(ctx, input, previousRequests)
		if err != nil {
			logger.LogError("ExecuteStream", err, slog.String("phase", "prepare_messages"))
			sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:      "error",
				Error:     errors.NewError(errors.EC_PREPARE_MESSAGES_FAILED.Code, "failed to prepare messages").Wrap(err),
				StopCause: types.StopCauseFromChatError(err),
			})
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
	prevMsgs := 0
	if len(previousRequests) > 0 {
		prevMsgs = 1 + len(previousRequests)
	}
	estimatedSize := 1 + len(history) + prevMsgs
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

	// 压缩摘要注入：若 memory 实现了可选 GetSummary 接口且摘要非空，插在
	// system 之后、history 之前。评审 R3：不改 MemoryProvider 接口体，
	// 用类型断言探测；L1 记忆在前、摘要在后（Anthropic 合并时顺序稳定）。
	if ae.memory != nil {
		if gs, ok := ae.memory.(interface{ GetSummary(context.Context) (string, error) }); ok {
			if summary, sErr := gs.GetSummary(ctx); sErr == nil && summary != "" {
				messages = append(messages, types.Message{
					Role:    "system",
					Content: "Previous conversation summary:\n" + summary,
				})
			}
		}
	}

	budgetCap := 0
	budgetTrim := false
	if config != nil {
		if config.MaxBudgetTokens > 0 {
			budgetCap = config.MaxBudgetTokens
			budgetTrim = true
		}
		if config.RemainPromptTokens != nil {
			if r := config.RemainPromptTokens(); r >= 0 {
				budgetTrim = true
				if budgetCap > 0 {
					budgetCap = min(budgetCap, r)
				} else {
					budgetCap = r
				}
			}
		}
	}
	if budgetTrim {
		history = trimHistoryToTokenBudget(history, budgetCap, config, previousRequests, input)
	}
	history = repairLLMMessageToolOrdering(history)

	if len(history) > 0 {
		messages = append(messages, history...)
	}

	if len(previousRequests) > 0 {
		messages = append(messages, types.MessagesFromToolSteps("", previousRequests)...)
	}

	messages = append(messages, input.ToMessage("user"))

	return messages, nil
}

// repairLLMMessageToolOrdering fixes history after token trim or DB compress: drops leading orphan tools,
// drops tools not after an assistant with tool_calls, and strips tool_calls (and following partial tool
// block) when not every tool_call_id has a matching tool message (OpenAI 400 otherwise).
func repairLLMMessageToolOrdering(history []types.Message) []types.Message {
	if len(history) == 0 {
		return history
	}
	i := 0
	for i < len(history) && strings.EqualFold(history[i].Role, "tool") {
		i++
	}
	history = history[i:]
	if len(history) == 0 {
		return history
	}
	out := make([]types.Message, 0, len(history))
	k := 0
	for k < len(history) {
		m := history[k]
		if !strings.EqualFold(m.Role, "assistant") || len(m.ToolCalls) == 0 {
			if strings.EqualFold(m.Role, "tool") {
				if len(out) == 0 {
					k++
					continue
				}
				prev := out[len(out)-1]
				if !strings.EqualFold(prev.Role, "assistant") || len(prev.ToolCalls) == 0 {
					k++
					continue
				}
			}
			out = append(out, m)
			k++
			continue
		}
		need := make(map[string]struct{})
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				need[tc.ID] = struct{}{}
			}
		}
		if len(need) == 0 {
			mm := m
			mm.ToolCalls = nil
			out = append(out, mm)
			k++
			continue
		}
		tEnd := k + 1
		for tEnd < len(history) && strings.EqualFold(history[tEnd].Role, "tool") {
			tEnd++
		}
		toolRun := history[k+1 : tEnd]
		got := make(map[string]struct{})
		for _, t := range toolRun {
			if t.ToolCallID != "" {
				if _, ok := need[t.ToolCallID]; ok {
					got[t.ToolCallID] = struct{}{}
				}
			}
		}
		complete := true
		for id := range need {
			if _, ok := got[id]; !ok {
				complete = false
				break
			}
		}
		if !complete {
			mm := m
			mm.ToolCalls = nil
			out = append(out, mm)
			k = tEnd
			continue
		}
		out = append(out, m)
		out = append(out, toolRun...)
		k = tEnd
	}
	return out
}

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

		sortedToolCalls, _ := ae.prepareToolCalls(response.ToolCalls)
		exists, results := ae.runToolCallsByLayer(ctx, sortedToolCalls, toolExecutionTimeout)
		cfg := ae.getConfig()
		writeDir := ""
		if cfg != nil {
			writeDir = cfg.ToolResultWriteDir
		}
		toolCalls, intermediateSteps := ae.buildToolCallResults(sortedToolCalls, exists, results, writeDir, "")

		result.ToolCalls = toolCalls
		result.IntermediateSteps = intermediateSteps

		// Log iteration completion information
		logger.LogExecution("executeIteration", iteration,
			fmt.Sprintf("Iteration %d completed with %d tool calls", iteration+1, len(toolCalls)),
			slog.Int("tool_calls", len(toolCalls)),
			slog.Duration("duration", time.Since(startTime)))

		return result, len(intermediateSteps) > 0, nil
	}

	logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Iteration %d completed with no tool calls", iteration+1))
	return result, false, nil
}

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
	var doomState doomLoopState
	var iteration int
	var hitDoom bool
	var lastHasMore bool

	// Get hooks
	hookRunner := hooks.NewRunner(ae.getHooks(), "", "")

	for iteration = 0; iteration < maxIterations; iteration++ {
		// Call OnBeforeIteration hook
		if err := hookRunner.BeforeIteration(iteration); err != nil {
			logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1), slog.String("phase", "on_before_iteration"))
		}

		select {
		case <-ctx.Done():
			hookRunner.OnError(ctx.Err())
			sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:      "error",
				Error:     ctx.Err(),
				StopCause: types.StopCauseFromChatError(ctx.Err()),
			})
			return
		default:
		}

		iterationStartTime := time.Now()
		logger.LogExecution("executeStreamWithIterations", iteration,
			fmt.Sprintf("Starting streaming iteration %d/%d", iteration+1, maxIterations))

		iterationResult, hasMore, err := ae.executeStreamIteration(ctx, messages, resultChan, iteration)
		if err != nil {
			logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1))
			hookRunner.OnError(err)
			sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:      "error",
				Error:     errors.NewError(errors.EC_STREAM_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err),
				StopCause: types.StopCauseFromChatError(err),
			})
			return
		}
		lastHasMore = hasMore

		finalResult.Output = iterationResult.Output
		finalResult.Usage.PromptTokens += iterationResult.Usage.PromptTokens
		finalResult.Usage.CompletionTokens += iterationResult.Usage.CompletionTokens
		finalResult.Usage.TotalTokens += iterationResult.Usage.TotalTokens
		finalResult.Usage.CachedTokens += iterationResult.Usage.CachedTokens
		finalResult.Usage.CacheCreationTokens += iterationResult.Usage.CacheCreationTokens
		toolCalls = append(toolCalls, iterationResult.ToolCalls...)
		intermediateSteps = append(intermediateSteps, iterationResult.IntermediateSteps...)

		// Call OnAfterIteration hook
		hookRunner.AfterIteration(iteration, iterationResult)

		if !hasMore {
			logger.LogExecution("executeStreamWithIterations", iteration,
				"Streaming execution completed",
				slog.Int("total_iterations", iteration+1),
				slog.Duration("iteration_duration", time.Since(iterationStartTime)))
			break
		}

		doomState.appendSteps(iterationResult.IntermediateSteps)
		if doomState.shouldStop(doomThreshold, onDoomLoop) {
			logger.LogExecution("executeStreamWithIterations", iteration, "Doom loop detected, stopping")
			hitDoom = true
			break
		}

		if iteration+1 < maxIterations {
			logger.LogExecution("executeStreamWithIterations", iteration, "Preparing next iteration messages")
			messages = ae.buildNextMessages(messages, iterationResult)
		} else {
			logger.LogExecution("executeStreamWithIterations", iteration, "Reached maximum iterations")
		}
	}

	// Set final result's tool calls and intermediate steps
	finalResult.ToolCalls = toolCalls
	finalResult.IntermediateSteps = intermediateSteps

	switch {
	case hitDoom:
		finalResult.StopCause = types.AgentStopCauseDoomLoop
	case lastHasMore:
		finalResult.StopCause = types.AgentStopCauseMaxIterations
	}

	if ae.memory != nil && len(initialMessages) > 0 {
		chatRole := "user"
		if c := ae.getConfig(); c != nil && c.ChatMessageRole != "" {
			chatRole = c.ChatMessageRole
		}
		lastMsg := initialMessages[len(initialMessages)-1]
		ae.saveToMemoryAndMaybeCompress(ctx, map[string]interface{}{
			"input": lastMsg.Content, "role": chatRole, "parts": lastMsg.Parts,
		}, memoryOutputMap(finalResult, intermediateSteps))
	}

	// Update total usage
	ae.mu.Lock()
	ae.totalUsage.PromptTokens += finalResult.Usage.PromptTokens
	ae.totalUsage.CompletionTokens += finalResult.Usage.CompletionTokens
	ae.totalUsage.TotalTokens += finalResult.Usage.TotalTokens
	ae.totalUsage.CachedTokens += finalResult.Usage.CachedTokens
	ae.totalUsage.CacheCreationTokens += finalResult.Usage.CacheCreationTokens
	ae.mu.Unlock()

	logger.LogExecution("executeStreamWithIterations", 0, "Stream execution completed successfully",
		slog.Int("total_iterations", iteration+1),
		slog.Int("total_tools", len(toolCalls)),
		slog.Int("total_tokens", finalResult.Usage.TotalTokens))

	// Call OnAfterEnd hook
	hookRunner.AfterEnd(finalResult)

	sendStreamResult(ctx, resultChan, types.StreamResult{
		Type:   "end",
		Result: finalResult,
	})
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

	// Get hooks
	hookRunner := hooks.NewRunner(ae.getHooks(), "", "")

	if ae.model == nil {
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "LLM model provider is nil")
	}

	// Call OnBeforeLLMCall hook
	hookRunner.BeforeLLMCall(messages)

	stream, err := ae.model.ChatWithToolsStream(ctx, messages, tools)
	if err != nil {
		hookRunner.OnError(err)
		return nil, false, errors.NewError(errors.EC_STREAM_CHAT_FAILED.Code, "failed to chat with tools stream").Wrap(err)
	}

	var outputBuilder strings.Builder
	outputBuilder.Grow(2048)

	for msg := range stream {
		if msg.Usage != nil {
			result.Usage = *msg.Usage
		}
		switch msg.Type {
		case "chunk":
			outputBuilder.WriteString(msg.Content)
			if !sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:    "chunk",
				Content: msg.Content,
			}) {
				return nil, false, ctx.Err()
			}
		case "reasoning":
			if !sendStreamResult(ctx, resultChan, types.StreamResult{
				Type:    "reasoning",
				Content: msg.Reasoning,
			}) {
				return nil, false, ctx.Err()
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
			err := errors.NewError(errors.EC_STREAM_ERROR.Code, "stream error occurred").Wrap(fmt.Errorf("%s", msg.Error))
			hookRunner.OnError(err)
			return nil, false, err
		}
	}

	// Call OnAfterLLMCall hook with the response
	responseMsg := &types.Message{
		Content: outputBuilder.String(),
	}
	hookRunner.AfterLLMCall(responseMsg)

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

		sortedToolCalls, _ := ae.prepareToolCalls(toolCallsForSorting)
		existsStream, resultsStream := ae.runToolCallsByLayer(ctx, sortedToolCalls, toolExecutionTimeout)
		cfg := ae.getConfig()
		writeDir := ""
		if cfg != nil {
			writeDir = cfg.ToolResultWriteDir
		}
		streamToolCalls, intermediateSteps := ae.buildToolCallResults(sortedToolCalls, existsStream, resultsStream, writeDir, "streaming")

		result.ToolCalls = streamToolCalls
		result.IntermediateSteps = intermediateSteps

		logger.LogExecution("executeStreamIteration", iteration, "Tool execution completed",
			slog.Int("executed_tools", len(streamToolCalls)),
			slog.Int("intermediate_steps", len(intermediateSteps)))

		return result, len(intermediateSteps) > 0, nil
	}

	logger.LogExecution("executeStreamIteration", iteration, "No tool calls in this iteration")
	return result, false, nil
}
