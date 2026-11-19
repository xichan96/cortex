package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/xichan96/cortex/agent/model"
)

// ---- Wire request types ---------------------------------------------------

type wireRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      []wireContentBlock `json:"system,omitempty"`
	Messages    []wireMessage      `json:"messages"`
	Tools       []wireTool         `json:"tools,omitempty"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *wireThinking      `json:"thinking,omitempty"`
}

type wireThinking struct {
	Type         string `json:"type"` // "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

type wireMessage struct {
	Role    string             `json:"role"`
	Content []wireContentBlock `json:"content"`
}

// wireContentBlock is a union of the Anthropic content block shapes this
// provider emits. Fields are omitempty so each block marshals only its own
// discriminator's fields.
type wireContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image
	Source *wireImageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// thinking / redacted_thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// prompt caching
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

type wireImageSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type wireCacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "1h" for long retention
}

type wireTool struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	InputSchema  json.RawMessage   `json:"input_schema"`
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

// toolResultEmptyText is sent when a tool produced no output. Anthropic rejects
// empty tool_result blocks, so a stable placeholder is used instead.
const toolResultEmptyText = "(tool returned no output)"

// ---- Request construction -------------------------------------------------

// buildRequestBody converts a model.Request into the JSON body for
// POST /v1/messages.
func (p *Provider) buildRequestBody(req *model.Request) ([]byte, error) {
	modelID := strings.TrimSpace(req.Model.ID)
	if modelID == "" {
		return nil, modelErrorf("request model id is required")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	var system []wireContentBlock
	if len(req.System) > 0 {
		system = append(system, textBlocks(req.System)...)
	}
	messages := make([]wireMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem {
			// System content belongs in the top-level system field. The runtime
			// normally passes it via Request.System, but tolerate both shapes.
			system = append(system, textBlocks(message.Content)...)
			continue
		}
		messages = appendWireMessage(messages, convertMessage(message))
	}

	tools := make([]wireTool, 0, len(req.Tools))
	for _, decl := range req.Tools {
		name := strings.TrimSpace(decl.Name)
		if name == "" {
			continue
		}
		tools = append(tools, wireTool{
			Name:        name,
			Description: decl.Description,
			InputSchema: normalizeToolSchema(decl.Parameters),
		})
	}

	body := wireRequest{
		Model:       modelID,
		MaxTokens:   maxTokens,
		System:      system,
		Messages:    messages,
		Tools:       tools,
		Stream:      true,
		Temperature: req.Temperature,
	}

	if budget := thinkingBudget(req.Thinking); budget > 0 && req.Model.Reasoning {
		// Anthropic requires budget_tokens < max_tokens and at least 1024.
		if budget >= maxTokens {
			budget = maxTokens - 1
		}
		if budget >= 1024 {
			body.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
			// Extended thinking requires temperature to be unset (or 1). Silent
			// degradation is preferred over rejecting the request.
			body.Temperature = nil
		}
	}

	applyCacheBreakpoints(&body, req.CacheRetention)

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, modelErrorf("encode request: %w", err)
	}
	return encoded, nil
}

// convertMessage maps one model.Message onto an Anthropic wire message. The
// result has a stable role ("user" or "assistant"); empty messages are returned
// with no content and later dropped by appendWireMessage.
func convertMessage(message model.Message) wireMessage {
	role := "user"
	switch message.Role {
	case model.RoleAssistant:
		role = "assistant"
	case model.RoleTool:
		role = "user" // tool_result blocks travel in user messages
	}

	blocks := make([]wireContentBlock, 0, len(message.Content))
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			if block.Text == "" {
				continue
			}
			blocks = append(blocks, wireContentBlock{Type: "text", Text: block.Text})

		case model.ContentImage:
			if converted, ok := convertImage(block.Image); ok {
				blocks = append(blocks, converted)
			}

		case model.ContentThinking:
			if role != "assistant" {
				continue
			}
			switch {
			case block.Redacted:
				blocks = append(blocks, wireContentBlock{
					Type: "redacted_thinking",
					Data: block.ThinkingSignature,
				})
			case block.ThinkingSignature != "":
				blocks = append(blocks, wireContentBlock{
					Type:      "thinking",
					Thinking:  block.Thinking,
					Signature: block.ThinkingSignature,
				})
			case block.Thinking != "":
				// Anthropic rejects assistant thinking blocks without a
				// signature on replay. Keep the text as plain text instead of
				// failing the whole request (e.g. after an aborted stream).
				blocks = append(blocks, wireContentBlock{Type: "text", Text: block.Thinking})
			}

		case model.ContentToolCall:
			if role != "assistant" || block.ToolCall == nil || block.ToolCall.ID == "" {
				continue
			}
			input := json.RawMessage(`{}`)
			if len(block.ToolCall.Arguments) > 0 {
				if encoded, err := json.Marshal(block.ToolCall.Arguments); err == nil {
					input = encoded
				}
			}
			blocks = append(blocks, wireContentBlock{
				Type:  "tool_use",
				ID:    block.ToolCall.ID,
				Name:  block.ToolCall.Name,
				Input: input,
			})

		case model.ContentToolResult:
			if role != "user" || block.ToolResult == nil {
				continue
			}
			text := block.ToolResult.Text
			if strings.TrimSpace(text) == "" {
				text = toolResultEmptyText
			}
			blocks = append(blocks, wireContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ToolResult.ToolCallID,
				Content:   text,
				IsError:   block.ToolResult.IsError,
			})
		}
	}

	return wireMessage{Role: role, Content: blocks}
}

func convertImage(image *model.ImageContent) (wireContentBlock, bool) {
	if image == nil {
		return wireContentBlock{}, false
	}
	if image.Data != "" {
		mediaType := image.MIMEType
		if mediaType == "" {
			mediaType = defaultImageMediaType
		}
		return wireContentBlock{
			Type: "image",
			Source: &wireImageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      image.Data,
			},
		}, true
	}
	if image.URL != "" {
		return wireContentBlock{
			Type:   "image",
			Source: &wireImageSource{Type: "url", URL: image.URL},
		}, true
	}
	return wireContentBlock{}, false
}

// textBlocks collects text content blocks for the system prompt.
func textBlocks(blocks []model.ContentBlock) []wireContentBlock {
	out := make([]wireContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != model.ContentText || block.Text == "" {
			continue
		}
		out = append(out, wireContentBlock{Type: "text", Text: block.Text})
	}
	return out
}

// appendWireMessage appends msg, merging it into the previous message when the
// roles match. Anthropic requires alternating roles, and tool results must
// follow their tool_use without an intervening message.
func appendWireMessage(messages []wireMessage, msg wireMessage) []wireMessage {
	if len(msg.Content) == 0 {
		return messages
	}
	if len(messages) > 0 && messages[len(messages)-1].Role == msg.Role {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, msg.Content...)
		return messages
	}
	return append(messages, msg)
}

// normalizeToolSchema guarantees the root is an object schema, which Anthropic
// requires. Unknown/absent schemas degrade to an empty object rather than
// failing the request.
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

// applyCacheBreakpoints places up to three prompt-cache breakpoints: the system
// block, the last tool definition and the last conversation block. Anthropic
// allows at most four; this provider never exceeds that budget.
func applyCacheBreakpoints(body *wireRequest, retention model.CacheRetention) {
	switch retention {
	case model.CacheRetentionShort, model.CacheRetentionLong:
	default:
		return
	}
	control := &wireCacheControl{Type: "ephemeral"}
	if retention == model.CacheRetentionLong {
		control.TTL = "1h"
	}

	used := 0
	if n := len(body.System); n > 0 {
		body.System[n-1].CacheControl = control
		used++
	}
	if n := len(body.Tools); n > 0 && used < 4 {
		body.Tools[n-1].CacheControl = control
		used++
	}
	if n := len(body.Messages); n > 0 && used < 4 {
		last := &body.Messages[n-1]
		if k := len(last.Content); k > 0 {
			switch last.Content[k-1].Type {
			case "text", "image", "tool_result":
				last.Content[k-1].CacheControl = control
			}
		}
	}
}
