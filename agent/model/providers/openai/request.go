package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/model"
)

// ---- Wire request types ---------------------------------------------------

type wireRequest struct {
	Model               string             `json:"model"`
	Messages            []wireMessage      `json:"messages"`
	Tools               []wireTool         `json:"tools,omitempty"`
	Stream              bool               `json:"stream"`
	StreamOptions       *wireStreamOptions `json:"stream_options,omitempty"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireMessage is one chat message. Content is either a string or a slice of
// wireContentPart; omitting it is valid only for assistant messages that carry
// tool_calls.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string            `json:"type"`
	Function wireFunctionEntry `json:"function"`
}

type wireFunctionEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---- Request construction -------------------------------------------------

// buildRequestBody converts a model.Request into the JSON body for
// POST /chat/completions.
func (p *Provider) buildRequestBody(req *model.Request) ([]byte, error) {
	modelID := strings.TrimSpace(req.Model.ID)
	if modelID == "" {
		return nil, fmt.Errorf("openai: request model id is required")
	}

	// System prompts are hoisted into a single leading system message. This
	// mirrors Request.System semantics and tolerates callers that put system
	// blocks in Messages instead.
	var systemParts []string
	for _, block := range req.System {
		if block.Type == model.ContentText && block.Text != "" {
			systemParts = append(systemParts, block.Text)
		}
	}

	messages := make([]wireMessage, 0, len(req.Messages)+1)
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem {
			for _, block := range message.Content {
				if block.Type == model.ContentText && block.Text != "" {
					systemParts = append(systemParts, block.Text)
				}
			}
			continue
		}
		messages = append(messages, convertMessage(message)...)
	}
	if len(systemParts) > 0 {
		system := wireMessage{Role: "system", Content: strings.Join(systemParts, "\n")}
		messages = append([]wireMessage{system}, messages...)
	}

	tools := make([]wireTool, 0, len(req.Tools))
	for _, decl := range req.Tools {
		name := strings.TrimSpace(decl.Name)
		if name == "" {
			continue
		}
		tools = append(tools, wireTool{
			Type: "function",
			Function: wireFunctionEntry{
				Name:        name,
				Description: decl.Description,
				Parameters:  normalizeToolSchema(decl.Parameters),
			},
		})
	}

	body := wireRequest{
		Model:         modelID,
		Messages:      messages,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &wireStreamOptions{IncludeUsage: true},
		Temperature:   req.Temperature,
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens > 0 {
		// Reasoning models reject max_tokens on the official OpenAI endpoint;
		// they take max_completion_tokens instead. Some compatible endpoints
		// require the same mapping, selected via PreferMaxCompletionTokens.
		if usesMaxCompletionTokens(modelID) || p.preferMaxCompletionTokens {
			body.MaxCompletionTokens = maxTokens
		} else {
			body.MaxTokens = maxTokens
		}
	}

	if p.reasoningEffort && req.Model.Reasoning {
		body.ReasoningEffort = mapReasoningEffort(req.Thinking)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	return encoded, nil
}

// convertMessage maps one model.Message onto one or more OpenAI wire messages.
// Tool results expand to one "tool" message per result because the protocol
// carries a single tool_call_id per message.
func convertMessage(message model.Message) []wireMessage {
	switch message.Role {
	case model.RoleAssistant:
		return convertAssistantMessage(message)
	case model.RoleTool:
		return convertToolMessages(message)
	default:
		return convertUserMessage(message)
	}
}

func convertUserMessage(message model.Message) []wireMessage {
	var textParts []string
	var parts []wireContentPart
	hasImage := false
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			if block.Text == "" {
				continue
			}
			textParts = append(textParts, block.Text)
			parts = append(parts, wireContentPart{Type: "text", Text: block.Text})
		case model.ContentImage:
			if image, ok := convertImage(block.Image); ok {
				parts = append(parts, image)
				hasImage = true
			}
		}
	}
	if len(textParts) == 0 && !hasImage {
		return nil
	}
	var content any
	if hasImage {
		content = parts
	} else {
		content = strings.Join(textParts, "\n")
	}
	return []wireMessage{{Role: "user", Content: content}}
}

func convertAssistantMessage(message model.Message) []wireMessage {
	var texts []string
	var toolCalls []wireToolCall
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case model.ContentToolCall:
			if block.ToolCall == nil || block.ToolCall.ID == "" {
				continue
			}
			arguments := "{}"
			if len(block.ToolCall.Arguments) > 0 {
				if encoded, err := json.Marshal(block.ToolCall.Arguments); err == nil {
					arguments = string(encoded)
				}
			}
			toolCalls = append(toolCalls, wireToolCall{
				ID:   block.ToolCall.ID,
				Type: "function",
				Function: wireFunctionCall{
					Name:      block.ToolCall.Name,
					Arguments: arguments,
				},
			})
			// Thinking blocks are not replayed: OpenAI does not accept reasoning
			// content in the transcript, and DeepSeek instructs clients to drop
			// reasoning_content from subsequent turns. Silently omit.
		}
	}
	if len(texts) == 0 && len(toolCalls) == 0 {
		return nil
	}
	wire := wireMessage{Role: "assistant", ToolCalls: toolCalls}
	if len(texts) > 0 {
		wire.Content = strings.Join(texts, "\n")
	}
	return []wireMessage{wire}
}

func convertToolMessages(message model.Message) []wireMessage {
	var out []wireMessage
	for _, block := range message.Content {
		if block.Type != model.ContentToolResult || block.ToolResult == nil {
			continue
		}
		// Content is always emitted, including the empty string: a tool message
		// without content is rejected by the protocol.
		out = append(out, wireMessage{
			Role:       "tool",
			ToolCallID: block.ToolResult.ToolCallID,
			Content:    block.ToolResult.Text,
		})
	}
	return out
}

func convertImage(image *model.ImageContent) (wireContentPart, bool) {
	if image == nil {
		return wireContentPart{}, false
	}
	url := image.URL
	if image.Data != "" {
		mediaType := image.MIMEType
		if mediaType == "" {
			mediaType = defaultImageMediaType
		}
		url = "data:" + mediaType + ";base64," + image.Data
	}
	if url == "" {
		return wireContentPart{}, false
	}
	return wireContentPart{Type: "image_url", ImageURL: &wireImageURL{URL: url}}, true
}

// normalizeToolSchema guarantees the root is an object schema, which the
// function-calling protocol expects. Unknown/absent schemas degrade to an empty
// object rather than failing the request.
func normalizeToolSchema(parameters json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	if len(parameters) == 0 {
		return empty
	}
	var schema map[string]any
	if err := json.Unmarshal(parameters, &schema); err != nil || schema == nil {
		return empty
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if _, ok := schema["properties"]; !ok {
		schema["properties"] = map[string]any{}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return empty
	}
	return encoded
}

// usesMaxCompletionTokens reports whether the model is from the o-series, which
// requires max_completion_tokens instead of max_tokens.
func usesMaxCompletionTokens(modelID string) bool {
	if len(modelID) < 2 || modelID[0] != 'o' {
		return false
	}
	return modelID[1] >= '1' && modelID[1] <= '9'
}

// mapReasoningEffort maps a provider-neutral level onto OpenAI's
// reasoning_effort values. The empty/off level omits the field so the model's
// default applies.
func mapReasoningEffort(level model.ThinkingLevel) string {
	switch level {
	case model.ThinkingMinimal, model.ThinkingLow:
		return "low"
	case model.ThinkingMedium:
		return "medium"
	case model.ThinkingHigh, model.ThinkingXHigh, model.ThinkingMax:
		return "high"
	default:
		return ""
	}
}
