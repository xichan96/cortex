package transcript

import (
	"encoding/json"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
)

// The wire format for a types.Message. Parts are encoded as a tagged array
// because types.MessagePart is an interface; a plain json.Unmarshal into
// types.Message would fail. Content is always emitted as a JSON string so the
// stored format stays stable even though legacy input may carry an array.
type messageJSON struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Parts      json.RawMessage  `json:"parts,omitempty"`
	Usage      *types.Usage     `json:"usage,omitempty"`
}

type contentBlockJSON struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ImageURL  *imageURLJSON  `json:"image_url,omitempty"`
	ImageData *imageDataJSON `json:"image_data,omitempty"`
}

type imageURLJSON struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type imageDataJSON struct {
	Data     []byte `json:"data"`
	MIMEType string `json:"mime_type"`
}

func marshalMessage(m types.Message) (json.RawMessage, error) {
	content, err := json.Marshal(m.Content)
	if err != nil {
		return nil, err
	}
	w := messageJSON{
		Role:       m.Role,
		Content:    content,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if len(m.Parts) > 0 {
		parts, err := marshalContentBlocks(m.Parts)
		if err != nil {
			return nil, err
		}
		w.Parts = parts
	}
	if !isZeroUsage(m.Usage) {
		usage := m.Usage
		w.Usage = &usage
	}
	return json.Marshal(w)
}

func unmarshalMessage(raw json.RawMessage) (types.Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return types.Message{}, fmt.Errorf("transcript: message is missing")
	}
	var w messageJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return types.Message{}, err
	}
	if w.Role == "" {
		return types.Message{}, fmt.Errorf("transcript: message role is required")
	}

	msg := types.Message{
		Role:       w.Role,
		Name:       w.Name,
		ToolCalls:  w.ToolCalls,
		ToolCallID: w.ToolCallID,
	}
	if w.Usage != nil {
		msg.Usage = *w.Usage
	}
	if len(w.Content) > 0 && string(w.Content) != "null" {
		var text string
		if err := json.Unmarshal(w.Content, &text); err == nil {
			msg.Content = text
		} else {
			parts, perr := unmarshalContentBlocks(w.Content)
			if perr != nil {
				return types.Message{}, fmt.Errorf("decode message content: %w", perr)
			}
			msg.Parts = parts
		}
	}
	if len(w.Parts) > 0 && string(w.Parts) != "null" {
		parts, err := unmarshalContentBlocks(w.Parts)
		if err != nil {
			return types.Message{}, fmt.Errorf("decode message parts: %w", err)
		}
		msg.Parts = parts
	}
	return msg, nil
}

// marshalContentBlocks encodes content blocks with a "type" discriminant.
func marshalContentBlocks(parts []ContentBlock) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]contentBlockJSON, 0, len(parts))
	for _, part := range parts {
		switch p := part.(type) {
		case types.TextPart:
			out = append(out, contentBlockJSON{Type: "text", Text: p.Text})
		case types.ImageURLPart:
			out = append(out, contentBlockJSON{
				Type:     "image_url",
				ImageURL: &imageURLJSON{URL: p.URL, Detail: p.Detail},
			})
		case types.ImageDataPart:
			out = append(out, contentBlockJSON{
				Type:      "image_data",
				ImageData: &imageDataJSON{Data: p.Data, MIMEType: p.MIMEType},
			})
		default:
			return nil, fmt.Errorf("transcript: unsupported content block %T", part)
		}
	}
	return json.Marshal(out)
}

// unmarshalContentBlocks decodes tagged content blocks. Unknown types are an
// error rather than a silent drop, so corrupt lines get a diagnostic.
func unmarshalContentBlocks(raw json.RawMessage) ([]ContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// A plain string is accepted for hand-written custom messages.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return nil, nil
		}
		return []ContentBlock{types.TextPart{Text: text}}, nil
	}

	var blocks []contentBlockJSON
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	out := make([]ContentBlock, 0, len(blocks))
	for i, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, types.TextPart{Text: b.Text})
		case "image_url":
			if b.ImageURL == nil {
				return nil, fmt.Errorf("content block %d: image_url payload is missing", i)
			}
			out = append(out, types.ImageURLPart{URL: b.ImageURL.URL, Detail: b.ImageURL.Detail})
		case "image_data":
			if b.ImageData == nil {
				return nil, fmt.Errorf("content block %d: image_data payload is missing", i)
			}
			// json.Unmarshal already validated the base64 payload into bytes.
			out = append(out, types.ImageDataPart{Data: b.ImageData.Data, MIMEType: b.ImageData.MIMEType})
		default:
			return nil, fmt.Errorf("content block %d: unknown type %q", i, b.Type)
		}
	}
	return out, nil
}

func isZeroUsage(u types.Usage) bool {
	return u.PromptTokens == 0 &&
		u.CompletionTokens == 0 &&
		u.TotalTokens == 0 &&
		u.ReasoningTokens == 0 &&
		u.CachedTokens == 0 &&
		u.CacheCreationTokens == 0
}
