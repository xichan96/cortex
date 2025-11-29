package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/errors"
	"github.com/xichan96/cortex/agent/logger"
	"github.com/xichan96/cortex/agent/types"
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
	SetToolRetryAttempts(attempts int)
	SetToolRetryDelay(delay time.Duration)
	SetEnableContextWindow(enable bool)
	SetContextWindowSize(size int)
	SetEnableFunctionCalling(enable bool)
	SetParallelToolCalls(enable bool)
	SetToolCallTimeout(timeout time.Duration)
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
	isRunning bool               // Running state
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

// SetToolRetryAttempts sets the number of tool retry attempts
func (ae *AgentEngine) SetToolRetryAttempts(attempts int) {
	ae.setConfigValue(func() {
		ae.config.ToolRetryAttempts = attempts
	})
}

// SetToolRetryDelay sets the tool retry delay
func (ae *AgentEngine) SetToolRetryDelay(delay time.Duration) {
	ae.setConfigValue(func() {
		ae.config.ToolRetryDelay = delay
	})
}

// SetEnableContextWindow sets whether to enable context window
func (ae *AgentEngine) SetEnableContextWindow(enable bool) {
	ae.setConfigValue(func() {
		ae.config.EnableContextWindow = enable
	})
}

// SetContextWindowSize sets the context window size
func (ae *AgentEngine) SetContextWindowSize(size int) {
	ae.setConfigValue(func() {
		ae.config.ContextWindowSize = size
	})
}

// SetEnableFunctionCalling sets whether to enable function calling
func (ae *AgentEngine) SetEnableFunctionCalling(enable bool) {
	ae.setConfigValue(func() {
		ae.config.EnableFunctionCalling = enable
	})
}

// SetParallelToolCalls sets whether to enable parallel tool calls
func (ae *AgentEngine) SetParallelToolCalls(enable bool) {
	ae.setConfigValue(func() {
		ae.config.ParallelToolCalls = enable
	})
}

// SetToolCallTimeout sets the tool call timeout
func (ae *AgentEngine) SetToolCallTimeout(timeout time.Duration) {
	ae.setConfigValue(func() {
		ae.config.ToolCallTimeout = timeout
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
	// Use write lock to set running state
	ae.mu.Lock()
	if ae.isRunning {
		ae.mu.Unlock()
		return nil, errors.EC_AGENT_BUSY
	}
	ae.isRunning = true
	ae.mu.Unlock()

	// Ensure state is reset after execution completes
	defer func() {
		ae.mu.Lock()
		ae.isRunning = false
		ae.mu.Unlock()
	}()

	// Add execution tracking
	startTime := time.Now()
	ae.logger.LogExecution("Execute", 0, "Starting agent execution",
		slog.String("input", truncateString(input, 100)),
		slog.Int("previousRequests", len(previousRequests)))

	// Pre-allocate slice capacity to reduce memory reallocations
	messages, err := ae.prepareMessages(input, previousRequests)
	if err != nil {
		ae.logger.LogError("Execute", err, slog.String("phase", "prepare_messages"))
		return nil, errors.NewAgentError(errors.EC_PREPARE_MESSAGES_FAILED.Code, errors.EC_PREPARE_MESSAGES_FAILED.Message).Wrap(err)
	}

	var finalResult *AgentResult
	iteration := 0
	maxIterations := ae.config.MaxIterations

	// Iterate until no tool calls or maximum iterations reached
	for iteration < maxIterations {
		ae.logger.LogExecution("Execute", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, maxIterations))

		// Execute single iteration
		result, continueIterating, err := ae.executeIteration(messages, iteration)
		if err != nil {
			ae.logger.LogError("Execute", err, slog.Int("iteration", iteration+1))
			return nil, errors.NewAgentError(errors.EC_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err)
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
	// 使用写锁来设置运行状态
	ae.mu.Lock()
	if ae.isRunning {
		ae.mu.Unlock()
		return nil, errors.EC_AGENT_BUSY
	}
	ae.isRunning = true
	ae.mu.Unlock()

	resultChan := make(chan StreamResult, DefaultChannelBuffer) // Using constant-defined buffer size

	go func() {
		defer close(resultChan)

		startTime := time.Now()
		ae.logger.LogExecution("ExecuteStream", 0, "Starting stream execution", slog.String("input", truncateString(input, 100)), slog.Int("previousRequests", len(previousRequests)))

		// 确保执行结束后重置状态
		defer func() {
			if r := recover(); r != nil {
				ae.logger.LogError("ExecuteStream", fmt.Errorf("panic recovered: %v", r))
				resultChan <- StreamResult{
					Type:  "error",
					Error: errors.NewAgentError(errors.EC_STREAM_PANIC.Code, "panic in stream execution").Wrap(fmt.Errorf("%v", r)),
				}
			}
			ae.mu.Lock()
			ae.isRunning = false
			ae.mu.Unlock()
		}()

		// 准备初始消息
		messages, err := ae.prepareMessages(input, previousRequests)
		if err != nil {
			ae.logger.LogError("ExecuteStream", err, slog.String("phase", "prepare_messages"))
			resultChan <- StreamResult{
				Type:  "error",
				Error: errors.NewAgentError(errors.EC_PREPARE_MESSAGES_FAILED.Code, "failed to prepare messages").Wrap(err),
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
	// get chat history to accurately pre-allocate capacity
	var history []types.Message
	var historyErr error
	if ae.memory != nil {
		history, historyErr = ae.memory.GetChatHistory()
		if historyErr != nil {
			return nil, fmt.Errorf("failed to get chat history: %w", historyErr)
		}
	}

	// Precisely pre-allocate slice capacity
	estimatedSize := 1 + // user message
		len(history) + // chat history
		len(previousRequests) // tool call context
	if ae.config.SystemMessage != "" {
		estimatedSize++ // system message
	}

	messages := make([]types.Message, 0, estimatedSize)

	// Add system message if configured
	if ae.config.SystemMessage != "" {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: ae.config.SystemMessage,
		})
	}

	// Add chat history if available
	if len(history) > 0 {
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

	// Add user input (using "human" role to match API requirements)
	messages = append(messages, types.Message{
		Role:    "human",
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
	startTime := time.Now()
	ae.logger.LogExecution("executeIteration", iteration, fmt.Sprintf("Starting iteration %d/%d", iteration+1, ae.config.MaxIterations))

	// Call LLM provider to get response with tool support
	response, err := ae.model.ChatWithTools(messages, ae.tools)
	if err != nil {
		ae.logger.LogError("executeIteration", err, slog.Int("iteration", iteration))
		return nil, false, errors.NewAgentError(errors.EC_CHAT_FAILED.Code, "failed to chat with tools").Wrap(err)
	}

	result := &AgentResult{
		Output: response.Content,
	}

	// Handle tool calls
	if len(response.ToolCalls) > 0 {
		fmt.Printf("LLM requested to call %d tools\n", len(response.ToolCalls))

		// Check if it's the last iteration, if so, skip tool execution
		if iteration+1 >= ae.config.MaxIterations {
			fmt.Printf("Iteration %d: Reached maximum iterations, skipping tool execution\n", iteration+1)
			return result, false, nil
		}

		toolCalls := make([]types.ToolCallRequest, 0, len(response.ToolCalls))
		intermediateSteps := make([]types.ToolCallData, 0, len(response.ToolCalls))

		for _, toolCall := range response.ToolCalls {
			fmt.Printf("Executing tool: %s\n", toolCall.Function.Name)

			// Use map for fast tool lookup
			tool, exists := ae.toolsMap[toolCall.Function.Name]
			if !exists {
				fmt.Printf("Tool %s not found\n", toolCall.Function.Name)
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

			fmt.Printf("Tool %s executed successfully, result: %v\n", toolCall.Function.Name, toolResult)

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
	// Only keep initial system message and user's original question
	// Pre-allocate slice capacity, usually only need system message and user message
	messages := make([]types.Message, 0, 2)

	// Keep system message (if any)
	for _, msg := range previousMessages {
		if msg.Role == "system" {
			messages = append(messages, msg)
		}
	}

	// Keep user's original question (last user message)
	for i := len(previousMessages) - 1; i >= 0; i-- {
		if previousMessages[i].Role == "user" {
			messages = append(messages, previousMessages[i])
			break
		}
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

	// Smarter pre-allocation: based on maximum iterations and average tool calls
	estimatedToolCalls := ae.config.MaxIterations * 3 // Assume average of 3 tool calls per round
	toolCalls := make([]types.ToolCallRequest, 0, estimatedToolCalls)
	intermediateSteps := make([]types.ToolCallData, 0, estimatedToolCalls)

	for iteration := 0; iteration < ae.config.MaxIterations; iteration++ {
		iterationStartTime := time.Now()
		ae.logger.LogExecution("executeStreamWithIterations", iteration,
			fmt.Sprintf("Starting streaming iteration %d/%d", iteration+1, ae.config.MaxIterations))

		// Execute single round iteration with streaming
		iterationResult, hasMore, err := ae.executeStreamIteration(messages, resultChan, iteration)
		if err != nil {
			ae.logger.LogError("executeStreamWithIterations", err, slog.Int("iteration", iteration+1))
			resultChan <- StreamResult{
				Type:  "error",
				Error: errors.NewAgentError(errors.EC_STREAM_ITERATION_FAILED.Code, fmt.Sprintf("iteration %d failed", iteration+1)).Wrap(err),
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

		// Prepare next round messages - only prepare if there are more iterations
		if iteration+1 < ae.config.MaxIterations {
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

	// 使用LLM提供商流式调用工具
	stream, err := ae.model.ChatWithToolsStream(messages, ae.tools)
	if err != nil {
		return nil, false, errors.NewAgentError(errors.EC_STREAM_CHAT_FAILED.Code, "failed to chat with tools stream").Wrap(err)
	}

	toolCalls := []types.ToolCallRequest{}
	intermediateSteps := []types.ToolCallData{}
	var hasToolCalls bool

	for msg := range stream {
		switch msg.Type {
		case "chunk":
			result.Output += msg.Content
			resultChan <- StreamResult{
				Type:    "chunk",
				Content: msg.Content,
			}
		case "tool_call":
			// Streaming interface supports tool call information
			// Note: Need to handle according to actual StreamMessage structure
			// Current StreamMessage has no ToolCall field, skip this case
			hasToolCalls = true
		case "error":
			return nil, false, errors.NewAgentError(errors.EC_STREAM_ERROR.Code, "stream error occurred").Wrap(fmt.Errorf("%s", msg.Error))
		}
	}

	// If streaming interface doesn't provide tool call information, use blocking call to get
	if !hasToolCalls && len(toolCalls) == 0 {
		response, err := ae.model.ChatWithTools(messages, ae.tools)
		if err != nil {
			return nil, false, errors.NewAgentError(errors.EC_BLOCKING_CHAT_FAILED.Code, "failed to get tool calls").Wrap(err)
		}
		// Convert ToolCall to ToolCallRequest
		for _, tc := range response.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, types.ToolCallRequest{
				Tool:       tc.Function.Name,
				ToolInput:  tc.Function.Arguments,
				ToolCallID: tc.ID,
				Type:       tc.Type,
			})
		}
	}

	// Process tool calls if available
	if len(result.ToolCalls) > 0 {
		ae.logger.LogExecution("executeStreamIteration", iteration, "Processing tool calls",
			slog.Int("tool_count", len(result.ToolCalls)))

		// Check if it's the last iteration, skip tool execution if so
		if iteration+1 >= ae.config.MaxIterations {
			ae.logger.LogExecution("executeStreamIteration", iteration, "Reached maximum iterations, skipping tool execution")
			return result, false, nil
		}

		// Pre-allocate result slices
		toolResults := make([]interface{}, 0, len(result.ToolCalls))
		toolErrors := make([]error, 0, len(result.ToolCalls))

		for _, toolCall := range result.ToolCalls {
			ae.logger.LogExecution("executeStreamIteration", iteration, "Executing tool",
				slog.String("tool_name", toolCall.Tool))

			// Use map to quickly find tool
			tool, exists := ae.toolsMap[toolCall.Tool]
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
	// Simple key generation: tool name + parameter hash
	hasher := md5.New()
	hasher.Write([]byte(toolName))
	// Can add parameter serialization here, but for performance, only use tool name for now
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
	if !ae.config.EnableToolRetry { // If tool retry is disabled, also disable cache
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
	if !ae.config.EnableToolRetry { // 如果禁用工具重试，也禁用缓存
		return
	}

	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	// Simple LRU strategy: if cache is full, clear oldest entry
	if len(ae.toolCache) >= ae.toolCacheSize {
		var oldestKey string
		var oldestTime time.Time
		for key, entry := range ae.toolCache {
			if oldestKey == "" || entry.timestamp.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.timestamp
			}
		}
		if oldestKey != "" {
			delete(ae.toolCache, oldestKey)
		}
	}

	ae.toolCache[cacheKey] = toolCacheEntry{
		result:    result,
		err:       err,
		timestamp: time.Now(),
	}
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
	ae.isRunning = false
}
