package session

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventTypeMessage    EventType = "message"
	EventTypeThinking   EventType = "thinking"
	EventTypeToolCall   EventType = "tool_call"
	EventTypeToolResult EventType = "tool_result"
	EventTypeToolStart  EventType = "tool_start"
	EventTypeTokenUsage EventType = "token_usage"
	EventTypeError      EventType = "error"
	EventTypeDone       EventType = "done"
	EventTypeApproved   EventType = "approved"
	EventTypeApproval   EventType = "approval"
	EventTypePlan       EventType = "plan"
	EventTypePlanStep   EventType = "plan_step"
)

type Event struct {
	Type       EventType      `json:"type"`
	SessionID  string         `json:"session_id"`
	Content    string         `json:"content,omitempty"`
	Thinking   string         `json:"thinking,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolInput  map[string]any `json:"tool_input,omitempty"`
	ToolOutput any            `json:"tool_output,omitempty"`
	Error      string         `json:"error,omitempty"`
	Approved   bool           `json:"approved,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Plan       *Plan          `json:"plan,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

type Plan struct {
	Goal      string     `json:"goal,omitempty"`
	Steps     []PlanStep `json:"steps"`
	Reasoning string     `json:"reasoning,omitempty"`
	Approved  bool       `json:"approved"`
}

type PlanStep struct {
	Index     int                    `json:"index"`
	Tool      string                 `json:"tool"`
	Input     map[string]interface{} `json:"input"`
	Reasoning string                 `json:"reasoning"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

func (e *Event) IsMessage() bool    { return e.Type == EventTypeMessage }
func (e *Event) IsThinking() bool   { return e.Type == EventTypeThinking }
func (e *Event) IsToolCall() bool   { return e.Type == EventTypeToolCall }
func (e *Event) IsToolResult() bool { return e.Type == EventTypeToolResult }
func (e *Event) IsError() bool      { return e.Type == EventTypeError }
func (e *Event) IsDone() bool       { return e.Type == EventTypeDone }
func (e *Event) IsApproval() bool   { return e.Type == EventTypeApproval || e.Type == EventTypeApproved }
func (e *Event) IsTokenUsage() bool { return e.Type == EventTypeTokenUsage }
func (e *Event) IsPlan() bool       { return e.Type == EventTypePlan }

func (e *Event) String() string {
	if e == nil {
		return ""
	}
	data, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return string(data)
}
