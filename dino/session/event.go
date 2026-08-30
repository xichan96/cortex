package session

import (
	"encoding/json"
	"time"

	agenttypes "github.com/xichan96/cortex/agent/types"
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
	// EventTypeQuestion question 工具向用户提问（P2.1，异步事件模型）。
	// session 检测到 SentinelQuestionResult 时 emit；UI 经 onQuestion 回调拿到
	// 问题，随后用 AnswerQuestion 把回答注入（作为下一条 user 消息喂回）。
	EventTypeQuestion EventType = "question"
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
	StopCause  string         `json:"stop_cause,omitempty"`
	// Question question 工具向用户提问的内容（P2.1）。配套 QuestionID 供回答注入。
	QuestionID string `json:"question_id,omitempty"`
	Question   string `json:"question,omitempty"`
	// Source 事件来源（S4/B2，subagent-s3s4 §7.7）。默认 "user"（向后兼容，
	// 缺失字段 = user）。唤醒注入 turn = "subagent"，供消费方（client.go /
	// turn_observe.go）识别并折叠，避免"幽灵用户消息"喷到 UI 与 assistant-text。
	Source    EventSource `json:"source,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// EventSource 事件来源标记（S4/B2）。默认 user 保持现有行为。
type EventSource string

const (
	// EventSourceUser 用户输入触发的 turn（默认）。
	EventSourceUser EventSource = "user"
	// EventSourceSubagent 子代理完成唤醒注入的 turn。
	EventSourceSubagent EventSource = "subagent"
)

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

type Usage = agenttypes.Usage

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
