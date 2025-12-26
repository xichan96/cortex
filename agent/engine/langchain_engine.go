package engine

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/ratelimit"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

// LangChainAgentEngine LangChain agent engine
type LangChainAgentEngine struct {
	_                  Agent // Ensure LangChainAgentEngine implements Agent interface
	llm                types.LLMProvider
	mu                 sync.RWMutex
	tools              []types.Tool
	toolsMap           map[string]types.Tool
	systemPrompt       string
	memory             []types.Message
	maxHistoryMessages int
	logger             *logger.Logger
}

// NewLangChainAgentEngine creates a new LangChain agent engine
func NewLangChainAgentEngine(llm types.LLMProvider, systemPrompt string) *LangChainAgentEngine {
	return &LangChainAgentEngine{
		llm:                llm,
		tools:              make([]types.Tool, 0),
		toolsMap:           make(map[string]types.Tool),
		systemPrompt:       systemPrompt,
		memory:             make([]types.Message, 0),
		maxHistoryMessages: 100,
		logger:             logger.NewLogger(),
	}
}

// NewLangChainAgent creates a new LangChain agent instance (via interface)
func NewLangChainAgent(llm types.LLMProvider, systemPrompt string) Agent {
	return NewLangChainAgentEngine(llm, systemPrompt)
}

// AddTool adds a tool
func (e *LangChainAgentEngine) AddTool(tool types.Tool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tools = append(e.tools, tool)
	e.toolsMap[tool.Name()] = tool
}

// SetTools sets tools
func (e *LangChainAgentEngine) SetTools(tools []types.Tool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tools = tools
	e.toolsMap = make(map[string]types.Tool, len(tools))
	for _, tool := range tools {
		e.toolsMap[tool.Name()] = tool
	}
}

// BuildAgent builds the agent (simplified version)
func (e *LangChainAgentEngine) BuildAgent() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.systemPrompt != "" && len(e.memory) == 0 {
		e.memory = append(e.memory, types.Message{
			Role:    "system",
			Content: e.systemPrompt,
		})
	}
	return nil
}

// ExecuteSimple simple execution method (for backward compatibility)
func (e *LangChainAgentEngine) ExecuteSimple(input string) (string, error) {
	e.mu.Lock()
	e.memory = append(e.memory, types.Message{
		Role:    "user",
		Content: input,
	})
	e.limitMemoryLocked()
	tools := make([]types.Tool, len(e.tools))
	copy(tools, e.tools)
	memory := make([]types.Message, len(e.memory))
	copy(memory, e.memory)
	e.mu.Unlock()

	if e.llm == nil {
		return "", errors.NewError(errors.EC_LLM_CALL_FAILED.Code, "LLM provider is nil")
	}

	if len(tools) > 0 {
		response, err := e.llm.ChatWithTools(memory, tools)
		if err != nil {
			return "", errors.NewError(errors.EC_LLM_CALL_FAILED.Code, errors.EC_LLM_CALL_FAILED.Message).Wrap(err)
		}

		e.mu.Lock()
		e.memory = append(e.memory, response)
		e.limitMemoryLocked()
		e.mu.Unlock()

		if len(response.ToolCalls) > 0 {
			return e.handleToolCalls(response)
		}

		return response.Content, nil
	}

	response, err := e.llm.Chat(memory)
	if err != nil {
		return "", errors.NewError(errors.EC_LLM_CALL_FAILED.Code, errors.EC_LLM_CALL_FAILED.Message).Wrap(err)
	}

	e.mu.Lock()
	e.memory = append(e.memory, response)
	e.limitMemoryLocked()
	e.mu.Unlock()

	return response.Content, nil
}

// ExecuteStreamSimple simple streaming execution (for backward compatibility)
func (e *LangChainAgentEngine) ExecuteStreamSimple(input string) (<-chan string, error) {
	e.mu.Lock()
	e.memory = append(e.memory, types.Message{
		Role:    "user",
		Content: input,
	})
	memory := make([]types.Message, len(e.memory))
	copy(memory, e.memory)
	e.mu.Unlock()

	if e.llm == nil {
		outputChan := make(chan string, 1)
		outputChan <- "Error: LLM provider is nil"
		close(outputChan)
		return outputChan, nil
	}

	outputChan := make(chan string, 100)

	go func() {
		defer close(outputChan)

		stream, err := e.llm.ChatStream(memory)
		if err != nil {
			outputChan <- fmt.Sprintf("Error: %v", err)
			return
		}

		var fullContent strings.Builder
		for msg := range stream {
			if msg.Type == "chunk" {
				outputChan <- msg.Content
				fullContent.WriteString(msg.Content)
			} else if msg.Type == "error" {
				outputChan <- fmt.Sprintf("Error: %s", msg.Error)
				return
			}
		}

		e.mu.Lock()
		e.memory = append(e.memory, types.Message{
			Role:    "assistant",
			Content: fullContent.String(),
		})
		e.limitMemoryLocked()
		e.mu.Unlock()
	}()

	return outputChan, nil
}

// handleToolCalls handles tool calls
func (e *LangChainAgentEngine) handleToolCalls(response types.Message) (string, error) {
	results := make([]string, 0, len(response.ToolCalls))

	e.mu.RLock()
	toolsMap := make(map[string]types.Tool, len(e.toolsMap))
	for k, v := range e.toolsMap {
		toolsMap[k] = v
	}
	e.mu.RUnlock()

	for _, toolCall := range response.ToolCalls {
		tool, exists := toolsMap[toolCall.Function.Name]
		if !exists {
			results = append(results, fmt.Sprintf("Tool %s not found", toolCall.Function.Name))
			continue
		}

		result, err := tool.Execute(toolCall.Function.Arguments)
		if err != nil {
			results = append(results, fmt.Sprintf("Tool %s execution failed: %v", toolCall.Function.Name, err))
			continue
		}

		results = append(results, fmt.Sprintf("Tool %s execution result: %v", toolCall.Function.Name, result))
	}

	return strings.Join(results, "\n"), nil
}

// GetMemory gets memory
func (e *LangChainAgentEngine) GetMemory() []types.Message {
	e.mu.RLock()
	defer e.mu.RUnlock()
	memory := make([]types.Message, len(e.memory))
	copy(memory, e.memory)
	return memory
}

// ClearMemory clears memory
func (e *LangChainAgentEngine) ClearMemory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.memory = make([]types.Message, 0)
	if e.systemPrompt != "" {
		e.memory = append(e.memory, types.Message{
			Role:    "system",
			Content: e.systemPrompt,
		})
	}
}

// SetTemperature sets temperature parameter
func (e *LangChainAgentEngine) SetTemperature(temperature float32) {
	// LangChain engine will handle temperature parameter through model configuration
	if cfg, ok := e.llm.(interface{ SetTemperature(float32) }); ok {
		cfg.SetTemperature(temperature)
	}
}

// SetMaxTokens sets maximum tokens
func (e *LangChainAgentEngine) SetMaxTokens(maxTokens int) {
	if cfg, ok := e.llm.(interface{ SetMaxTokens(int) }); ok {
		cfg.SetMaxTokens(maxTokens)
	}
}

// SetTopP sets Top P sampling
func (e *LangChainAgentEngine) SetTopP(topP float32) {
	if cfg, ok := e.llm.(interface{ SetTopP(float32) }); ok {
		cfg.SetTopP(topP)
	}
}

// SetFrequencyPenalty sets frequency penalty
func (e *LangChainAgentEngine) SetFrequencyPenalty(penalty float32) {
	if cfg, ok := e.llm.(interface{ SetFrequencyPenalty(float32) }); ok {
		cfg.SetFrequencyPenalty(penalty)
	}
}

// SetPresencePenalty sets presence penalty
func (e *LangChainAgentEngine) SetPresencePenalty(penalty float32) {
	if cfg, ok := e.llm.(interface{ SetPresencePenalty(float32) }); ok {
		cfg.SetPresencePenalty(penalty)
	}
}

// SetStopSequences sets stop sequences
func (e *LangChainAgentEngine) SetStopSequences(sequences []string) {
	if cfg, ok := e.llm.(interface{ SetStopSequences([]string) }); ok {
		cfg.SetStopSequences(sequences)
	}
}

// SetTimeout sets timeout duration
func (e *LangChainAgentEngine) SetTimeout(timeout time.Duration) {
	if cfg, ok := e.llm.(interface{ SetTimeout(time.Duration) }); ok {
		cfg.SetTimeout(timeout)
	}
}

// SetRetryAttempts sets retry attempts
func (e *LangChainAgentEngine) SetRetryAttempts(attempts int) {
	if cfg, ok := e.llm.(interface{ SetRetryAttempts(int) }); ok {
		cfg.SetRetryAttempts(attempts)
	}
}

// SetRetryDelay sets retry delay
func (e *LangChainAgentEngine) SetRetryDelay(delay time.Duration) {
	if cfg, ok := e.llm.(interface{ SetRetryDelay(time.Duration) }); ok {
		cfg.SetRetryDelay(delay)
	}
}

// SetEnableToolRetry sets whether to enable tool retry
func (e *LangChainAgentEngine) SetEnableToolRetry(enable bool) {
	// Support determined by specific LLM implementation
}

// SetConfig sets complete configuration
func (e *LangChainAgentEngine) SetConfig(config *types.AgentConfig) {
	if config == nil {
		return
	}
	e.SetTemperature(config.Temperature)
	e.SetMaxTokens(config.MaxTokens)
	e.SetTopP(config.TopP)
	e.SetFrequencyPenalty(config.FrequencyPenalty)
	e.SetPresencePenalty(config.PresencePenalty)
	e.SetStopSequences(config.StopSequences)
	e.SetTimeout(config.Timeout)
	e.SetRetryAttempts(config.RetryAttempts)
	e.SetRetryDelay(config.RetryDelay)
	e.mu.Lock()
	e.maxHistoryMessages = config.MaxHistoryMessages
	e.limitMemoryLocked()
	e.mu.Unlock()
}

// SetRateLimiter sets the rate limiter (not implemented for LangChain engine)
func (e *LangChainAgentEngine) SetRateLimiter(limiter ratelimit.RateLimiter) {
	// LangChain engine does not implement rate limiting
}

// limitMemory limits memory size based on maxHistoryMessages
func (e *LangChainAgentEngine) limitMemory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.limitMemoryLocked()
}

// limitMemoryLocked limits memory size (must be called with lock held)
func (e *LangChainAgentEngine) limitMemoryLocked() {
	if e.maxHistoryMessages > 0 && len(e.memory) > e.maxHistoryMessages {
		keepSystem := false
		var systemMsg types.Message
		if len(e.memory) > 0 && e.memory[0].Role == "system" {
			keepSystem = true
			systemMsg = e.memory[0]
		}
		start := len(e.memory) - e.maxHistoryMessages
		if keepSystem {
			e.memory = append([]types.Message{systemMsg}, e.memory[start+1:]...)
		} else {
			e.memory = e.memory[start:]
		}
	}
}

// SetMemory sets memory system (LangChain engine uses internal memory management)
func (e *LangChainAgentEngine) SetMemory(memory types.MemoryProvider) {
	// LangChain engine uses internal memory management, this method is not implemented
}

// SetOutputParser sets output parser
func (e *LangChainAgentEngine) SetOutputParser(parser types.OutputParser) {
	// Support for output parser determined by specific implementation
}

// AddTools adds tools in batch
func (e *LangChainAgentEngine) AddTools(tools []types.Tool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tools = append(e.tools, tools...)
	for _, tool := range tools {
		e.toolsMap[tool.Name()] = tool
	}
}

// Execute executes the agent (implements Agent interface)
func (e *LangChainAgentEngine) Execute(input string, previousRequests []types.ToolCallData) (*AgentResult, error) {
	startTime := time.Now()
	e.logger.LogExecution("LangChainAgentEngine.Execute", 0, "Starting execution",
		slog.String("input", truncateString(input, 100)))

	// Adapt to Agent interface, ignore previousRequests parameter
	output, err := e.ExecuteSimple(input)
	if err != nil {
		e.logger.LogError("LangChainAgentEngine.Execute", err)
		return nil, err
	}

	executionTime := time.Since(startTime)
	e.logger.LogExecution("LangChainAgentEngine.Execute", 0, "Execution completed",
		slog.Duration("duration", executionTime))

	return &AgentResult{
		Output: output,
	}, nil
}

// ExecuteStream streams agent execution (implements Agent interface)
func (e *LangChainAgentEngine) ExecuteStream(input string, previousRequests []types.ToolCallData) (<-chan StreamResult, error) {
	// Adapt to Agent interface, ignore previousRequests parameter
	outputChan, err := e.ExecuteStreamSimple(input)
	if err != nil {
		return nil, err
	}

	resultChan := make(chan StreamResult, 100)

	go func() {
		defer close(resultChan)

		for content := range outputChan {
			resultChan <- StreamResult{
				Type:    "chunk",
				Content: content,
			}
		}

		resultChan <- StreamResult{
			Type: "end",
		}
	}()

	return resultChan, nil
}

// Stop stops the agent engine (LangChain engine requires no special stop operation)
func (e *LangChainAgentEngine) Stop() {
	// LangChain engine requires no special stop operation
}
