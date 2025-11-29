package engine

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// LangChainAgentEngine LangChain agent engine
type LangChainAgentEngine struct {
	_            Agent // Ensure LangChainAgentEngine implements Agent interface
	llm          types.LLMProvider
	tools        []types.Tool
	toolsMap     map[string]types.Tool // Tool map for optimized lookup performance
	systemPrompt string
	memory       []types.Message
}

// NewLangChainAgentEngine creates a new LangChain agent engine
func NewLangChainAgentEngine(llm types.LLMProvider, systemPrompt string) *LangChainAgentEngine {
	return &LangChainAgentEngine{
		llm:          llm,
		tools:        make([]types.Tool, 0),
		toolsMap:     make(map[string]types.Tool),
		systemPrompt: systemPrompt,
		memory:       make([]types.Message, 0),
	}
}

// NewLangChainAgent creates a new LangChain agent instance (via interface)
func NewLangChainAgent(llm types.LLMProvider, systemPrompt string) Agent {
	return NewLangChainAgentEngine(llm, systemPrompt)
}

// AddTool adds a tool
func (e *LangChainAgentEngine) AddTool(tool types.Tool) {
	e.tools = append(e.tools, tool)
	e.toolsMap[tool.Name()] = tool
}

// SetTools sets tools
func (e *LangChainAgentEngine) SetTools(tools []types.Tool) {
	e.tools = tools
	e.toolsMap = make(map[string]types.Tool, len(tools))
	for _, tool := range tools {
		e.toolsMap[tool.Name()] = tool
	}
}

// BuildAgent builds the agent (simplified version)
func (e *LangChainAgentEngine) BuildAgent() error {
	// Add system prompt to memory
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
	// Add user message to memory
	e.memory = append(e.memory, types.Message{
		Role:    "user",
		Content: input,
	})

	// Use tool calling if tools are available
	if len(e.tools) > 0 {
		response, err := e.llm.ChatWithTools(e.memory, e.tools)
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		// Add assistant response to memory
		e.memory = append(e.memory, response)

		// Handle tool calls
		if len(response.ToolCalls) > 0 {
			return e.handleToolCalls(response)
		}

		return response.Content, nil
	}

	// Regular chat
	response, err := e.llm.Chat(e.memory)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	// Add assistant response to memory
	e.memory = append(e.memory, response)

	return response.Content, nil
}

// ExecuteStreamSimple simple streaming execution (for backward compatibility)
func (e *LangChainAgentEngine) ExecuteStreamSimple(input string) (<-chan string, error) {
	// Add user message to memory
	e.memory = append(e.memory, types.Message{
		Role:    "user",
		Content: input,
	})

	outputChan := make(chan string, 100)

	go func() {
		defer close(outputChan)

		// Streaming call
		stream, err := e.llm.ChatStream(e.memory)
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

		// Add assistant response to memory
		e.memory = append(e.memory, types.Message{
			Role:    "assistant",
			Content: fullContent.String(),
		})
	}()

	return outputChan, nil
}

// handleToolCalls handles tool calls
func (e *LangChainAgentEngine) handleToolCalls(response types.Message) (string, error) {
	// Pre-allocate slice capacity to reduce memory reallocations
	results := make([]string, 0, len(response.ToolCalls))

	for _, toolCall := range response.ToolCalls {
		// Use map for fast tool lookup
		tool, exists := e.toolsMap[toolCall.Function.Name]
		if !exists {
			results = append(results, fmt.Sprintf("Tool %s not found", toolCall.Function.Name))
			continue
		}

		// Execute tool
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
	return e.memory
}

// ClearMemory clears memory
func (e *LangChainAgentEngine) ClearMemory() {
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

// SetToolRetryAttempts sets tool retry attempts
func (e *LangChainAgentEngine) SetToolRetryAttempts(attempts int) {
	// Support determined by specific LLM implementation
}

// SetToolRetryDelay sets tool retry delay
func (e *LangChainAgentEngine) SetToolRetryDelay(delay time.Duration) {
	// Support determined by specific LLM implementation
}

// SetEnableContextWindow sets whether to enable context window
func (e *LangChainAgentEngine) SetEnableContextWindow(enable bool) {
	// Support determined by specific LLM implementation
}

// SetContextWindowSize sets context window size
func (e *LangChainAgentEngine) SetContextWindowSize(size int) {
	// Support determined by specific LLM implementation
}

// SetEnableFunctionCalling sets whether to enable function calling
func (e *LangChainAgentEngine) SetEnableFunctionCalling(enable bool) {
	// Support determined by specific LLM implementation
}

// SetParallelToolCalls sets whether to enable parallel tool calls
func (e *LangChainAgentEngine) SetParallelToolCalls(enable bool) {
	// Support determined by specific LLM implementation
}

// SetToolCallTimeout sets tool call timeout
func (e *LangChainAgentEngine) SetToolCallTimeout(timeout time.Duration) {
	// Support determined by specific LLM implementation
}

// SetConfig sets complete configuration
func (e *LangChainAgentEngine) SetConfig(config *types.AgentConfig) {
	// 设置所有支持的参数
	e.SetTemperature(config.Temperature)
	e.SetMaxTokens(config.MaxTokens)
	e.SetTopP(config.TopP)
	e.SetFrequencyPenalty(config.FrequencyPenalty)
	e.SetPresencePenalty(config.PresencePenalty)
	e.SetStopSequences(config.StopSequences)
	e.SetTimeout(config.Timeout)
	e.SetRetryAttempts(config.RetryAttempts)
	e.SetRetryDelay(config.RetryDelay)
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
	e.tools = append(e.tools, tools...)
	for _, tool := range tools {
		e.toolsMap[tool.Name()] = tool
	}
}

// Execute executes the agent (implements Agent interface)
func (e *LangChainAgentEngine) Execute(input string, previousRequests []types.ToolCallData) (*AgentResult, error) {
	startTime := time.Now()
	log.Printf("[LangChainAgentEngine] Starting execution with input: %s", truncateString(input, 100))

	// Adapt to Agent interface, ignore previousRequests parameter
	output, err := e.ExecuteSimple(input)
	if err != nil {
		log.Printf("[LangChainAgentEngine] Execution failed: %v", err)
		return nil, err
	}

	executionTime := time.Since(startTime)
	log.Printf("[LangChainAgentEngine] Execution completed in %v", executionTime)

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
