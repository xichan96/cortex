package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/xichan96/cortex/agent/types"
)

// LangChainLLMProvider LangChain LLM provider
type LangChainLLMProvider struct {
	model     llms.Model
	modelName string
}

// NewLangChainLLMProvider creates a new LangChain LLM provider
func NewLangChainLLMProvider(model llms.Model, modelName string) *LangChainLLMProvider {
	return &LangChainLLMProvider{
		model:     model,
		modelName: modelName,
	}
}

// Chat basic chat functionality
func (p *LangChainLLMProvider) Chat(messages []types.Message) (types.Message, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	// Set maximum retry count
	maxRetries := 3
	retryCount := 0
	// Define regular expression
	retryAfterRegex := regexp.MustCompile(`Please retry after (\d+) milliseconds`)

	for {
		// Call LLM
		response, err := p.model.GenerateContent(context.Background(), langChainMessages)
		if err != nil {
			// Check if it's a 429 error
			errMsg := err.Error()
			if strings.Contains(errMsg, "429") && retryCount < maxRetries {
				// Extract suggested wait time
				matches := retryAfterRegex.FindStringSubmatch(errMsg)
				waitTime := 5000 // Default wait time is 5 seconds
				if len(matches) > 1 {
					if parsedTime, err := strconv.Atoi(matches[1]); err == nil {
						waitTime = parsedTime
					}
				}

				// Wait for specified time
				fmt.Printf("Received 429 error, waiting %d milliseconds before retry... (Attempt %d/%d)\n", waitTime, retryCount+1, maxRetries)
				time.Sleep(time.Duration(waitTime) * time.Millisecond)
				retryCount++
				continue
			}

			// Not a 429 error or max retries exceeded
			return types.Message{}, err
		}

		// 转换响应
		if len(response.Choices) > 0 {
			return p.convertMessageFromLangChain(response.Choices[0]), nil
		}

		return types.Message{}, fmt.Errorf("No response content")
	}
}

// ChatStream streaming chat functionality
func (p *LangChainLLMProvider) ChatStream(messages []types.Message) (<-chan types.StreamMessage, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	outputChan := make(chan types.StreamMessage, 100)

	go func() {
		defer close(outputChan)

		// Set maximum retry count
		maxRetries := 3
		retryCount := 0

		// Regular expression to extract wait time
		retryAfterRegex := regexp.MustCompile(`Please retry after (\d+) milliseconds`)

		for retryCount <= maxRetries {
			if retryCount > 0 {
				outputChan <- types.StreamMessage{
					Type:    "retry",
					Content: fmt.Sprintf("Retrying after 429 error (attempt %d/%d)", retryCount, maxRetries),
				}
			}

			// Streaming call
			_, err := p.model.GenerateContent(context.Background(), langChainMessages, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
				outputChan <- types.StreamMessage{
					Type:    "chunk",
					Content: string(chunk),
				}
				return nil
			}))

			if err != nil {
				// Check if it's a 429 error
				errMsg := err.Error()
				if strings.Contains(errMsg, "429") && retryCount < maxRetries {
					// Extract suggested wait time
					matches := retryAfterRegex.FindStringSubmatch(errMsg)
					waitTime := 5000 // Default wait time is 5 seconds
					if len(matches) > 1 {
						if parsedTime, err := strconv.Atoi(matches[1]); err == nil {
							waitTime = parsedTime
						}
					}

					// Wait for specified time before retry
					outputChan <- types.StreamMessage{
						Type:    "info",
						Content: fmt.Sprintf("Received 429 error, waiting %d milliseconds before retry...", waitTime),
					}
					time.Sleep(time.Duration(waitTime) * time.Millisecond)
					retryCount++
					continue
				}

				// Not a 429 error or max retries exceeded
				outputChan <- types.StreamMessage{
					Type:  "error",
					Error: errMsg,
				}
				return
			}

			// Successfully completed, send end signal
			outputChan <- types.StreamMessage{Type: "end"}
			break
		}
	}()

	return outputChan, nil
}

// ChatWithTools chat with tools functionality
func (p *LangChainLLMProvider) ChatWithTools(messages []types.Message, tools []types.Tool) (types.Message, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	// Convert tools
	langChainTools := p.convertToLangChainTools(tools)

	// Set maximum retry count
	maxRetries := 3
	retryCount := 0
	// Define regular expression
	retryAfterRegex := regexp.MustCompile(`Please retry after (\d+) milliseconds`)

	for {
		// Call LLM
		response, err := p.model.GenerateContent(context.Background(), langChainMessages, llms.WithTools(langChainTools))
		if err != nil {
			// Check if it's a 429 error
			errMsg := err.Error()
			if strings.Contains(errMsg, "429") && retryCount < maxRetries {
				// Extract suggested wait time
				matches := retryAfterRegex.FindStringSubmatch(errMsg)
				waitTime := 5000 // Default wait time is 5 seconds
				if len(matches) > 1 {
					if parsedTime, err := strconv.Atoi(matches[1]); err == nil {
						waitTime = parsedTime
					}
				}

				// Wait for specified time before retry
				fmt.Printf("Received 429 error, waiting %d milliseconds before retry... (Attempt %d/%d)\n", waitTime, retryCount+1, maxRetries)
				time.Sleep(time.Duration(waitTime) * time.Millisecond)
				retryCount++
				continue
			}

			// Not a 429 error or max retries exceeded
			return types.Message{}, err
		}

		// Convert response
		if len(response.Choices) > 0 {
			return p.convertMessageFromLangChain(response.Choices[0]), nil
		}

		return types.Message{}, fmt.Errorf("No response content")
	}
}

// ChatWithToolsStream streaming chat with tools functionality
func (p *LangChainLLMProvider) ChatWithToolsStream(messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	// Convert tools
	langChainTools := p.convertToLangChainTools(tools)

	outputChan := make(chan types.StreamMessage, 100)

	go func() {
		defer close(outputChan)

		// Set maximum retry count
		maxRetries := 3
		retryCount := 0

		// Regular expression to extract wait time
		retryAfterRegex := regexp.MustCompile(`Please retry after (\d+) milliseconds`)

		for retryCount <= maxRetries {
			if retryCount > 0 {
				outputChan <- types.StreamMessage{
					Type:    "retry",
					Content: fmt.Sprintf("Retrying after 429 error (attempt %d/%d)", retryCount, maxRetries),
				}
			}

			var inToolCall bool

			// Streaming call
			_, err := p.model.GenerateContent(context.Background(), langChainMessages,
				llms.WithTools(langChainTools),
				llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
					chunkStr := string(chunk)

					// Detect start and end of tool calls
					if strings.Contains(chunkStr, "function_call") || strings.Contains(chunkStr, "tool_calls") {
						inToolCall = true
					}

					// If not in a tool call, send content
					if !inToolCall && chunkStr != "" {
						outputChan <- types.StreamMessage{
							Type:    "chunk",
							Content: chunkStr,
						}
					}

					// Detect end of tool call
					if inToolCall && (strings.Contains(chunkStr, "}") || strings.Contains(chunkStr, "]")) {
						inToolCall = false
					}

					return nil
				}))

			if err != nil {
				// Check if it's a 429 error
				errMsg := err.Error()
				if strings.Contains(errMsg, "429") && retryCount < maxRetries {
					// Extract suggested wait time
					matches := retryAfterRegex.FindStringSubmatch(errMsg)
					waitTime := 5000 // Default wait time is 5 seconds
					if len(matches) > 1 {
						if parsedTime, err := strconv.Atoi(matches[1]); err == nil {
							waitTime = parsedTime
						}
					}

					// Wait for specified time before retry
					outputChan <- types.StreamMessage{
						Type:    "info",
						Content: fmt.Sprintf("Received 429 error, waiting %d milliseconds before retry...", waitTime),
					}
					time.Sleep(time.Duration(waitTime) * time.Millisecond)
					retryCount++
					continue
				}

				// Not a 429 error or max retries exceeded
				outputChan <- types.StreamMessage{
					Type:  "error",
					Error: errMsg,
				}
				return
			}

			// Successfully completed, send end signal
			outputChan <- types.StreamMessage{Type: "end"}
			break
		}
	}()

	return outputChan, nil
}

// GetModelName gets the model name
func (p *LangChainLLMProvider) GetModelName() string {
	return p.modelName
}

// GetModelMetadata gets the model metadata
func (p *LangChainLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{
		Name:      p.modelName,
		Version:   "1.0.0",
		MaxTokens: 4096,
	}
}

// convertToLangChainMessages converts message format
func (p *LangChainLLMProvider) convertToLangChainMessages(messages []types.Message) []llms.MessageContent {
	langChainMessages := make([]llms.MessageContent, len(messages))
	for i, msg := range messages {
		// Map role types
		var role llms.ChatMessageType
		switch msg.Role {
		case "system":
			role = llms.ChatMessageTypeSystem
		case "user":
			role = llms.ChatMessageTypeHuman
		case "assistant":
			role = llms.ChatMessageTypeAI
		case "tool":
			role = llms.ChatMessageTypeTool
		case "function":
			role = llms.ChatMessageTypeFunction
		default:
			role = llms.ChatMessageTypeGeneric
		}

		// Build content parts
		var parts []llms.ContentPart

		// If there are multimodal parts, use Parts, otherwise use traditional Content field
		if len(msg.Parts) > 0 {
			for _, part := range msg.Parts {
				switch p := part.(type) {
				case types.TextPart:
					parts = append(parts, llms.TextPart(p.Text))
				case types.ImageURLPart:
					if p.Detail != "" {
						parts = append(parts, llms.ImageURLWithDetailPart(p.URL, p.Detail))
					} else {
						parts = append(parts, llms.ImageURLPart(p.URL))
					}
				case types.ImageDataPart:
					parts = append(parts, llms.BinaryPart(p.MIMEType, p.Data))
				}
			}
		} else if msg.Content != "" {
			// Backward compatibility: use traditional Content field
			parts = []llms.ContentPart{llms.TextPart(msg.Content)}
		}

		langChainMessages[i] = llms.MessageContent{
			Role:  role,
			Parts: parts,
		}
	}
	return langChainMessages
}

// convertToLangChainTools converts tool format
func (p *LangChainLLMProvider) convertToLangChainTools(tools []types.Tool) []llms.Tool {
	langChainTools := make([]llms.Tool, len(tools))
	for i, tool := range tools {
		langChainTools[i] = llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Schema(),
			},
		}
	}
	return langChainTools
}

// convertMessageFromLangChain converts message from LangChain
func (p *LangChainLLMProvider) convertMessageFromLangChain(choice *llms.ContentChoice) types.Message {
	msg := types.Message{
		Content: choice.Content,
	}

	// Set role if available
	if choice.FuncCall != nil || len(choice.ToolCalls) > 0 {
		msg.Role = "assistant"
	}

	// Convert tool calls
	if len(choice.ToolCalls) > 0 {
		msg.ToolCalls = make([]types.ToolCall, len(choice.ToolCalls))
		for i, tc := range choice.ToolCalls {
			// Parse argument string into map
			var args map[string]interface{}
			if tc.FunctionCall.Arguments != "" {
				json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args)
			}

			msg.ToolCalls[i] = types.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: types.ToolFunction{
					Name:      tc.FunctionCall.Name,
					Arguments: args,
				},
			}
		}
	}

	return msg
}
