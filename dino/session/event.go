package session

import (
	"encoding/json"
	"time"

	"github.com/xichan96/cortex/agent/question"
	agenttypes "github.com/xichan96/cortex/agent/types"
)

type EventType string

const (
	EventTypeMessage    EventType = "message"
	EventTypeThinking   EventType = "thinking"
	EventTypeToolCall   EventType = "tool_call"
	EventTypeToolResult EventType = "tool_result"
	EventTypeToolStart  EventType = "tool_start"
	// EventTypeToolInputDelta 工具参数 JSON 的增量片段（Anthropic input_json_delta）。
	// Content 是这一段原文，ToolCallID/ToolName 标识归属。消费方据此边收边渲染，
	// 不必等到参数解析完才显示工具卡片。
	EventTypeToolInputDelta EventType = "tool_input_delta"
	EventTypeTokenUsage     EventType = "token_usage"
	EventTypeError          EventType = "error"
	EventTypeDone           EventType = "done"
	EventTypeApproved       EventType = "approved"
	EventTypeApproval       EventType = "approval"
	EventTypePlan           EventType = "plan"
	EventTypePlanStep       EventType = "plan_step"
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
	// ApprovalPending 为 true 时，这次 EventTypeApproval 表示「请求尚在等待
	// 用户决议」（RequestID 可用于回灌），而不是「策略已给出结论」。
	ApprovalPending bool `json:"approval_pending,omitempty"`
	// RequestID 审批请求 ID（ApprovalPending 为 true 时有值）。
	// 消费方拿到后调 DinoFactory.RespondToolApproval(requestID, approved) 回灌。
	RequestID string `json:"request_id,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
	Plan      *Plan  `json:"plan,omitempty"`
	StopCause string `json:"stop_cause,omitempty"`
	// Question question 工具向用户提问的内容（P2.1）。配套 QuestionID 供回答注入。
	// 结构化提问时 Question 保留首题文本，兼容旧消费方。
	QuestionID string `json:"question_id,omitempty"`
	Question   string `json:"question,omitempty"`
	// Questions 是结构化的 1-3 个问题（id/header/question/options/multiSelect/
	// isSecret）。Dojo 按 questions[] 解析；旧消费方只看 Question 仍可用。
	Questions []question.Spec `json:"questions,omitempty"`
	// QuestionPending 为 true 表示本轮阻塞等待答案（同轮阻塞模式）。
	// 消费方用 QuestionID（请求 ID）调用 AnswerQuestionWithData 回灌；
	// false/缺失表示旧模式：答案通过 AnswerQuestion 作为新输入回流。
	QuestionPending bool `json:"question_pending,omitempty"`
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
