package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

// LangChainLLMProvider LangChain LLM provider
type LangChainLLMProvider struct {
	model      llms.Model
	modelName  string
	logger     *logger.Logger
	maxRetries int
	retryDelay time.Duration
}

// NewLangChainLLMProvider creates a new LangChain LLM provider
func NewLangChainLLMProvider(model llms.Model, modelName string) *LangChainLLMProvider {
	return &LangChainLLMProvider{
		model:      model,
		modelName:  modelName,
		logger:     logger.NewLogger(),
		maxRetries: 3,
		retryDelay: 1 * time.Second,
	}
}

// SetMaxRetries sets maximum retry attempts
func (p *LangChainLLMProvider) SetMaxRetries(maxRetries int) {
	p.maxRetries = maxRetries
}

// SetRetryDelay sets retry delay duration
func (p *LangChainLLMProvider) SetRetryDelay(delay time.Duration) {
	p.retryDelay = delay
}

// handle429Retry handles 429 rate limit errors with retry logic
func (p *LangChainLLMProvider) handle429Retry(err error, retryCount, maxRetries int) (shouldRetry bool, waitTime time.Duration) {
	if retryCount >= maxRetries {
		return false, 0
	}

	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "429") && !strings.Contains(errMsg, "rate limit") && !strings.Contains(errMsg, "rate_limit") && !strings.Contains(errMsg, "too many requests") {
		return false, 0
	}

	retryAfterRegex := regexp.MustCompile(`(?i)(?:retry|wait|after)[\s:]+(\d+)[\s]*?(milliseconds?|ms|seconds?|s|minutes?|m)`)
	matches := retryAfterRegex.FindStringSubmatch(err.Error())
	waitTime = p.retryDelay
	if len(matches) >= 3 {
		if parsedTime, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
			unit := strings.ToLower(strings.TrimSpace(matches[2]))
			switch {
			case strings.HasPrefix(unit, "milli") || unit == "ms":
				waitTime = time.Duration(parsedTime) * time.Millisecond
			case strings.HasPrefix(unit, "second") || unit == "s":
				waitTime = time.Duration(parsedTime) * time.Second
			case strings.HasPrefix(unit, "minute") || unit == "m":
				waitTime = time.Duration(parsedTime) * time.Minute
			default:
				waitTime = time.Duration(parsedTime) * time.Millisecond
			}
		}
	}

	p.logger.Info("Received 429 error, will retry after wait",
		slog.Duration("wait_time", waitTime),
		slog.Int("attempt", retryCount+1),
		slog.Int("max_retries", maxRetries))

	return true, waitTime
}

// Chat basic chat functionality
func (p *LangChainLLMProvider) Chat(messages []types.Message) (types.Message, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	retryCount := 0

	for {
		// Call LLM
		response, err := p.model.GenerateContent(context.Background(), langChainMessages)
		if err != nil {
			// Handle 429 retry
			if shouldRetry, waitTime := p.handle429Retry(err, retryCount, p.maxRetries); shouldRetry {
				retryCount++
				time.Sleep(waitTime)
				continue
			}

			// Not a 429 error or max retries exceeded
			return types.Message{}, err
		}

		if len(response.Choices) > 0 {
			return p.convertMessageFromLangChain(response.Choices[0]), nil
		}

		return types.Message{}, errors.EC_LLM_NO_RESPONSE
	}
}

// ChatStream streaming chat functionality
func (p *LangChainLLMProvider) ChatStream(messages []types.Message) (<-chan types.StreamMessage, error) {
	// Convert message format
	langChainMessages := p.convertToLangChainMessages(messages)

	outputChan := make(chan types.StreamMessage, 100)

	go func() {
		defer close(outputChan)

		retryCount := 0

		for {
			if retryCount > 0 {
				outputChan <- types.StreamMessage{
					Type:    "retry",
					Content: fmt.Sprintf("Retrying after 429 error (attempt %d/%d)", retryCount, p.maxRetries),
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
				// Handle 429 retry
				if shouldRetry, waitTime := p.handle429Retry(err, retryCount, p.maxRetries); shouldRetry {
					outputChan <- types.StreamMessage{
						Type:    "info",
						Content: fmt.Sprintf("Received 429 error, waiting %v before retry...", waitTime),
					}
					retryCount++
					time.Sleep(waitTime)
					continue
				}

				// Not a 429 error or max retries exceeded
				outputChan <- types.StreamMessage{
					Type:  "error",
					Error: err.Error(),
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

	retryCount := 0

	for {
		// Call LLM
		response, err := p.model.GenerateContent(context.Background(), langChainMessages, llms.WithTools(langChainTools))
		if err != nil {
			// Handle 429 retry
			if shouldRetry, waitTime := p.handle429Retry(err, retryCount, p.maxRetries); shouldRetry {
				retryCount++
				time.Sleep(waitTime)
				continue
			}

			// Not a 429 error or max retries exceeded
			return types.Message{}, err
		}

		// Convert response
		if len(response.Choices) > 0 {
			return p.convertMessageFromLangChain(response.Choices[0]), nil
		}

		return types.Message{}, errors.EC_LLM_NO_RESPONSE
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

		retryCount := 0

		for {
			if retryCount > 0 {
				outputChan <- types.StreamMessage{
					Type:    "retry",
					Content: fmt.Sprintf("Retrying after 429 error (attempt %d/%d)", retryCount, p.maxRetries),
				}
			}

			var fullResponse *llms.ContentResponse
			var contentBuffer strings.Builder
			contentBuffer.Grow(2048)

			// Streaming call
			// Note: We collect all content chunks and filter tool calls from the full response
			// This is more reliable than trying to detect tool calls in streaming chunks
			response, err := p.model.GenerateContent(context.Background(), langChainMessages,
				llms.WithTools(langChainTools),
				llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
					chunkStr := string(chunk)
					contentBuffer.WriteString(chunkStr)

					// Send content chunks immediately for better user experience
					// Tool calls will be filtered from the full response later
					if chunkStr != "" {
						outputChan <- types.StreamMessage{
							Type:    "chunk",
							Content: chunkStr,
						}
					}

					return nil
				}))

			// Save the full response to extract tool calls
			if err == nil {
				fullResponse = response
			}

			if err != nil {
				// Handle 429 retry
				if shouldRetry, waitTime := p.handle429Retry(err, retryCount, p.maxRetries); shouldRetry {
					outputChan <- types.StreamMessage{
						Type:    "info",
						Content: fmt.Sprintf("Received 429 error, waiting %v before retry...", waitTime),
					}
					retryCount++
					contentBuffer.Reset()
					time.Sleep(waitTime)
					continue
				}

				// Not a 429 error or max retries exceeded
				outputChan <- types.StreamMessage{
					Type:  "error",
					Error: err.Error(),
				}
				return
			}

			// Extract tool calls from full response if available
			if fullResponse != nil && len(fullResponse.Choices) > 0 {
				choice := fullResponse.Choices[0]
				if len(choice.ToolCalls) > 0 {
					toolCalls := make([]types.ToolCall, len(choice.ToolCalls))
					for i, tc := range choice.ToolCalls {
						var args map[string]interface{}
						if tc.FunctionCall.Arguments != "" {
							if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
								p.logger.LogError("ChatWithToolsStream", err, slog.String("tool", tc.FunctionCall.Name))
								args = make(map[string]interface{})
							}
						}
						toolCalls[i] = types.ToolCall{
							ID:   tc.ID,
							Type: tc.Type,
							Function: types.ToolFunction{
								Name:      tc.FunctionCall.Name,
								Arguments: args,
							},
						}
					}
					outputChan <- types.StreamMessage{
						Type:      "tool_calls",
						ToolCalls: toolCalls,
					}
				}
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
		if len(msg.Parts) > 0 {
			parts = make([]llms.ContentPart, 0, len(msg.Parts))
		} else {
			parts = make([]llms.ContentPart, 0, 1)
		}

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
			parts = append(parts, llms.TextPart(msg.Content))
		} else if msg.Content == "" && len(msg.ToolCalls) > 0 {
			// For assistant messages with tool calls but no content, use empty string
			parts = append(parts, llms.TextPart(""))
		}

		// Ensure content is never null - provide empty string if no content exists
		// This is required by some APIs that expect content to be a string, not null
		if len(parts) == 0 {
			// For tool messages, use ToolCallResponse if ToolCallID is present
			if msg.Role == "tool" && msg.ToolCallID != "" {
				// Ensure Content is never null - use empty string if not provided
				content := msg.Content
				if content == "" {
					content = "{}"
				}
				parts = append(parts, llms.ToolCallResponse{
					ToolCallID: msg.ToolCallID,
					Name:       msg.Name,
					Content:    content,
				})
			} else {
				// For other messages, use empty text part
				parts = append(parts, llms.TextPart(""))
			}
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
	// Content will be empty string if not provided (Go zero value), which is acceptable
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
				if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
					p.logger.LogError("convertMessageFromLangChain", err, slog.String("tool", tc.FunctionCall.Name))
					args = make(map[string]interface{})
				}
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
