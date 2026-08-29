package dino

import (
	"strings"
	"time"

	"github.com/xichan96/cortex/dino/session"
)

type ExecuteRequest struct {
	SessionID         string
	Content           string
	Files             []FileAttachment
	ExtraSystemPrompt string
	Stream            bool
}

type FileAttachment struct {
	Path    string
	Name    string
	Content []byte
}

type StreamEventSender interface {
	SendStreamEvent(sessionID string, event interface{})
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

type Session = session.Session
type SessionOption = session.Option

type Event = session.Event
type EventType = session.EventType
type EventSource = session.EventSource
type Usage = session.Usage
type Observer = session.Observer
type ObserverFunc = session.ObserverFunc
type OutputObserver = session.OutputObserver
type ExecuteResponse = session.ExecuteResponse
type ToolCallInfo = session.ToolCallInfo
type Plan = session.Plan
type PlanStep = session.PlanStep
type sessionConfig = session.Config

const (
	EventTypeMessage    = session.EventTypeMessage
	EventTypeThinking   = session.EventTypeThinking
	EventTypeToolCall   = session.EventTypeToolCall
	EventTypeToolResult = session.EventTypeToolResult
	EventTypeToolStart  = session.EventTypeToolStart
	EventTypeTokenUsage = session.EventTypeTokenUsage
	EventTypeError      = session.EventTypeError
	EventTypeDone       = session.EventTypeDone
	EventTypeApproved   = session.EventTypeApproved
	EventTypeApproval   = session.EventTypeApproval
	EventTypePlan       = session.EventTypePlan
	EventTypePlanStep   = session.EventTypePlanStep
	EventTypeQuestion   = session.EventTypeQuestion

	StreamEventContent    = session.EventTypeMessage
	StreamEventReasoning  = session.EventTypeThinking
	StreamEventToolCall   = session.EventTypeToolCall
	StreamEventToolResult = session.EventTypeToolResult
	StreamEventError      = session.EventTypeError
	StreamEventDone       = session.EventTypeDone
	StreamEventApproval   = session.EventTypeApproval

	EventSourceUser    = session.EventSourceUser
	EventSourceSubagent = session.EventSourceSubagent
)

type StreamEvent = session.Event

func WithInputBufferSize(size int) SessionOption {
	return session.WithInputBufferSize(size)
}

func WithOutputBufferSize(size int) SessionOption {
	return session.WithOutputBufferSize(size)
}

func WithQueueEnabled(maxSize, maxPending int) SessionOption {
	return session.WithQueueEnabled(maxSize, maxPending)
}

func WithPlannerEnabled(enabled bool, prompt string, autoApprove bool) SessionOption {
	return session.WithPlannerEnabled(enabled, prompt, autoApprove)
}

type Message struct {
	ID       string           `json:"id"`
	Role     MessageRole      `json:"role"`
	Parts    []Part           `json:"parts"`
	Metadata *MessageMetadata `json:"metadata,omitempty"`
}

type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

type MessageMetadata struct {
	Time      MessageTime         `json:"time"`
	Error     *MessageError       `json:"error,omitempty"`
	SessionID string              `json:"session_id"`
	Tool      map[string]ToolMeta `json:"tool,omitempty"`
	Assistant *AssistantMeta      `json:"assistant,omitempty"`
	Snapshot  string              `json:"snapshot,omitempty"`
}

type MessageTime struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed,omitempty"`
}

type MessageError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type ToolMeta struct {
	Title    string                 `json:"title"`
	Snapshot string                 `json:"snapshot,omitempty"`
	Time     ToolMetaTime           `json:"time"`
	Extra    map[string]interface{} `json:"extra,omitempty"`
}

type ToolMetaTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}

type AssistantMeta struct {
	System   []string      `json:"system"`
	ModelID  string        `json:"model_id"`
	Provider string        `json:"provider"`
	Path     AssistantPath `json:"path"`
	Cost     float64       `json:"cost"`
	Summary  bool          `json:"summary,omitempty"`
	Tokens   TokenInfo     `json:"tokens"`
}

type AssistantPath struct {
	CWD  string `json:"cwd"`
	Root string `json:"root"`
}

type TokenInfo struct {
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     TokenCache `json:"cache"`
}

type TokenCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type Part interface {
	isPart()
}

type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (p TextPart) isPart() {}

type ReasoningPart struct {
	Type             string                 `json:"type"`
	Text             string                 `json:"text"`
	ProviderMetadata map[string]interface{} `json:"provider_metadata,omitempty"`
}

func (p ReasoningPart) isPart() {}

type ToolInvocationPart struct {
	Type           string         `json:"type"`
	ToolInvocation ToolInvocation `json:"tool_invocation"`
}

func (p ToolInvocationPart) isPart() {}

type SourceURLPart struct {
	Type             string                 `json:"type"`
	SourceID         string                 `json:"source_id"`
	URL              string                 `json:"url"`
	Title            string                 `json:"title,omitempty"`
	ProviderMetadata map[string]interface{} `json:"provider_metadata,omitempty"`
}

func (p SourceURLPart) isPart() {}

type FilePart struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Filename  string `json:"filename,omitempty"`
	URL       string `json:"url"`
}

func (p FilePart) isPart() {}

type StepStartPart struct {
	Type string `json:"type"`
}

func (p StepStartPart) isPart() {}

type ToolInvocation struct {
	State      ToolInvocationState    `json:"state"`
	Step       int                    `json:"step,omitempty"`
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Args       map[string]interface{} `json:"args,omitempty"`
	Result     string                 `json:"result,omitempty"`
}

type ToolInvocationState string

const (
	ToolStateCall        ToolInvocationState = "call"
	ToolStatePartialCall ToolInvocationState = "partial-call"
	ToolStateResult      ToolInvocationState = "result"
)

func NewMessage(id string, role MessageRole, sessionID string) *Message {
	return &Message{
		ID:    id,
		Role:  role,
		Parts: make([]Part, 0),
		Metadata: &MessageMetadata{
			Time: MessageTime{
				Created: time.Now().UnixMilli(),
			},
			SessionID: sessionID,
		},
	}
}

func (m *Message) AddText(text string) {
	m.Parts = append(m.Parts, TextPart{
		Type: "text",
		Text: text,
	})
}

func (m *Message) AddReasoning(text string) {
	m.Parts = append(m.Parts, ReasoningPart{
		Type: "reasoning",
		Text: text,
	})
}

func (m *Message) AddToolCall(toolCallID, toolName string, args map[string]interface{}) {
	m.Parts = append(m.Parts, ToolInvocationPart{
		Type: "tool-invocation",
		ToolInvocation: ToolInvocation{
			State:      ToolStateCall,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Args:       args,
		},
	})
}

func (m *Message) AddToolResult(toolCallID, toolName, result string) {
	m.Parts = append(m.Parts, ToolInvocationPart{
		Type: "tool-invocation",
		ToolInvocation: ToolInvocation{
			State:      ToolStateResult,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Result:     result,
		},
	})
}

func (m *Message) Complete() {
	if m.Metadata != nil {
		m.Metadata.Time.Completed = time.Now().UnixMilli()
	}
}

func (m *Message) GetText() string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if tp, ok := p.(TextPart); ok {
			sb.WriteString(tp.Text)
		}
	}
	return sb.String()
}

func (m *Message) GetToolCalls() []ToolInvocation {
	var result []ToolInvocation
	for _, p := range m.Parts {
		if tp, ok := p.(ToolInvocationPart); ok {
			if tp.ToolInvocation.State == ToolStateCall {
				result = append(result, tp.ToolInvocation)
			}
		}
	}
	return result
}

func (m *Message) GetToolResults() []ToolInvocation {
	var result []ToolInvocation
	for _, p := range m.Parts {
		if tp, ok := p.(ToolInvocationPart); ok {
			if tp.ToolInvocation.State == ToolStateResult {
				result = append(result, tp.ToolInvocation)
			}
		}
	}
	return result
}

func (m *Message) GetReasoning() string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if rp, ok := p.(ReasoningPart); ok {
			sb.WriteString(rp.Text)
		}
	}
	return sb.String()
}
