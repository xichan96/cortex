package model

import (
	"context"
	"encoding/json"
)

// Role is a transcript message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentType discriminates ContentBlock values.
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentThinking   ContentType = "thinking"
	ContentImage      ContentType = "image"
	ContentToolCall   ContentType = "toolCall"
	ContentToolResult ContentType = "toolResult"
)

// ImageContent is an image input. Data is base64-encoded; URL is an
// alternative reference understood by some providers.
type ImageContent struct {
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ToolCall is a model-issued tool call. Arguments is always non-nil after a
// finished call; when the model's text was not a valid JSON object Arguments
// is empty and ArgumentsError describes the problem.
type ToolCall struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Arguments        map[string]any     `json:"arguments"`
	ArgumentsError   *ToolArgumentError `json:"argumentsError,omitempty"`
	ThoughtSignature string             `json:"thoughtSignature,omitempty"`
}

// ToolResult is a tool execution result carried in a message.
type ToolResult struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName,omitempty"`
	Text       string `json:"text,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// ContentBlock is one element of a message. Type selects which field is set.
type ContentBlock struct {
	Type              ContentType   `json:"type"`
	Text              string        `json:"text,omitempty"`
	Thinking          string        `json:"thinking,omitempty"`
	ThinkingSignature string        `json:"thinkingSignature,omitempty"`
	Redacted          bool          `json:"redacted,omitempty"`
	Image             *ImageContent `json:"image,omitempty"`
	ToolCall          *ToolCall     `json:"toolCall,omitempty"`
	ToolResult        *ToolResult   `json:"toolResult,omitempty"`
}

// Message is one transcript entry supplied to a provider.
type Message struct {
	Role      Role           `json:"role"`
	Content   []ContentBlock `json:"content"`
	Timestamp int64          `json:"timestamp,omitempty"` // Unix milliseconds
}

// ToolDeclaration declares a tool available to the model. Parameters is the
// JSON Schema of the arguments object.
type ToolDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Request is one provider call. Model must be a concrete physical model.
type Request struct {
	Model          *Model
	System         []ContentBlock
	Messages       []Message
	Tools          []ToolDeclaration
	Thinking       ThinkingLevel
	MaxTokens      int
	Temperature    *float64
	CacheRetention CacheRetention
	Headers        map[string]string
}

// Provider turns requests into streams. Models returns the provider's static
// catalog entries; the runtime may layer overrides on top.
type Provider interface {
	ID() string
	Models() []Model
	Stream(ctx context.Context, req *Request) (Stream, error)
}

// Stream is a single in-flight response. Events delivers the discriminated
// stream event sequence and is closed by the provider. Result is authoritative
// once Events is closed and remains valid after Close.
type Stream interface {
	Events() <-chan StreamEvent
	Result() (*AssistantMessage, error)
	Close() error
}

// StopReason is the terminal status of an assistant message.
type StopReason string

const (
	StopReasonPending  StopReason = "pending"
	StopReasonStop     StopReason = "stop"
	StopReasonLength   StopReason = "length"
	StopReasonToolUse  StopReason = "toolUse"
	StopReasonError    StopReason = "error"
	StopReasonAborted  StopReason = "aborted"
	StopReasonDeferred StopReason = "deferred"
)

// IsTerminal reports whether the reason ends the response.
func (r StopReason) IsTerminal() bool {
	return r != "" && r != StopReasonPending
}

// IsError reports whether the reason represents a failed or aborted response.
func (r StopReason) IsError() bool {
	return r == StopReasonError || r == StopReasonAborted
}

// Usage reports token accounting for one response. Input is the total input
// (uncached + cache read + cache write). Reasoning is a subset of Output.
type Usage struct {
	Input        int       `json:"input"`
	Output       int       `json:"output"`
	CacheRead    int       `json:"cacheRead"`
	CacheWrite   int       `json:"cacheWrite"`
	CacheWrite1h int       `json:"cacheWrite1h,omitempty"`
	Reasoning    int       `json:"reasoning,omitempty"`
	TotalTokens  int       `json:"totalTokens"`
	Cost         UsageCost `json:"cost"`
}

// UsageCost is token cost in USD. Total is either the provider-reported billed
// amount (TotalFromProvider) or a catalog estimate.
type UsageCost struct {
	Input             float64 `json:"input"`
	Output            float64 `json:"output"`
	CacheRead         float64 `json:"cacheRead"`
	CacheWrite        float64 `json:"cacheWrite"`
	Total             float64 `json:"total"`
	TotalFromProvider bool    `json:"totalFromProvider,omitempty"`
}

// Add accumulates another usage record into u.
func (u *Usage) Add(other Usage) {
	u.Input += other.Input
	u.Output += other.Output
	u.CacheRead += other.CacheRead
	u.CacheWrite += other.CacheWrite
	u.CacheWrite1h += other.CacheWrite1h
	u.Reasoning += other.Reasoning
	u.TotalTokens += other.TotalTokens
	u.Cost.Input += other.Cost.Input
	u.Cost.Output += other.Cost.Output
	u.Cost.CacheRead += other.Cost.CacheRead
	u.Cost.CacheWrite += other.Cost.CacheWrite
	u.Cost.Total += other.Cost.Total
	u.Cost.TotalFromProvider = u.Cost.TotalFromProvider || other.Cost.TotalFromProvider
}

// AssistantMessage is the authoritative completed response.
type AssistantMessage struct {
	Role          Role           `json:"role"`
	Content       []ContentBlock `json:"content"`
	API           API            `json:"api,omitempty"`
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	ResponseModel string         `json:"responseModel,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
	ThinkingLevel ThinkingLevel  `json:"thinkingLevel,omitempty"`
	Usage         Usage          `json:"usage"`
	StopReason    StopReason     `json:"stopReason"`
	ErrorMessage  string         `json:"errorMessage,omitempty"`
	Timestamp     int64          `json:"timestamp,omitempty"` // Unix milliseconds
}

// Text returns the concatenated text blocks of the message.
func (m *AssistantMessage) Text() string {
	if m == nil {
		return ""
	}
	var text string
	for _, block := range m.Content {
		if block.Type == ContentText {
			text += block.Text
		}
	}
	return text
}

// ToolCalls returns the tool-call blocks of the message in order.
func (m *AssistantMessage) ToolCalls() []ToolCall {
	if m == nil {
		return nil
	}
	var calls []ToolCall
	for _, block := range m.Content {
		if block.Type == ContentToolCall && block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	return calls
}

// ToolCallDelta is the payload of toolcall_start/delta/end events. Start
// carries ID/Name; delta carries raw JSON text; end carries the parsed call.
type ToolCallDelta struct {
	Index          int            `json:"index"`
	ID             string         `json:"id,omitempty"`
	Name           string         `json:"name,omitempty"`
	ArgumentsDelta string         `json:"argumentsDelta,omitempty"`
	Arguments      map[string]any `json:"arguments,omitempty"`
}
