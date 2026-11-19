package runtime

import (
	"encoding/json"
	"time"
)

// EventType is the discriminator of a runtime event.
type EventType string

// Event types. Lifecycle, turn, message, tool, queue, compaction, retry, entry
// and interaction events are all emitted by the session core, never by a
// transport.
const (
	EventSessionStart EventType = "session_start"
	EventSessionEnd   EventType = "session_end"

	EventAgentStart   EventType = "agent_start"
	EventAgentEnd     EventType = "agent_end"
	EventAgentSettled EventType = "agent_settled"

	EventTurnStart EventType = "turn_start"
	EventTurnEnd   EventType = "turn_end"

	EventMessageStart  EventType = "message_start"
	EventMessageUpdate EventType = "message_update"
	EventMessageEnd    EventType = "message_end"

	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"

	EventQueueUpdate EventType = "queue_update"

	EventCompactionStart  EventType = "compaction_start"
	EventCompactionEnd    EventType = "compaction_end"
	EventCompactionFailed EventType = "compaction_failed"

	EventRetryStart EventType = "retry_start"
	EventRetryEnd   EventType = "retry_end"

	EventEntryAppended EventType = "entry_appended"

	EventApprovalRequest  EventType = "approval_request"
	EventApprovalResponse EventType = "approval_response"

	EventModelChanged         EventType = "model_changed"
	EventThinkingLevelChanged EventType = "thinking_level_changed"
)

// Event is one record in the session event stream. Seq is a monotonically
// increasing per-session counter; Payload is event-type specific.
type Event struct {
	Type      EventType       `json:"type"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// DecodePayload unmarshals the event payload into v. A nil payload is a no-op.
func (e Event) DecodePayload(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, v)
}

// Payload types used by the core. Drivers may define their own payload shapes
// for events they own; transports forward them verbatim.

// SessionStartPayload describes a new session.
type SessionStartPayload struct {
	Model string `json:"model,omitempty"`
}

// SessionEndPayload describes why a session ended.
type SessionEndPayload struct {
	Reason string `json:"reason,omitempty"`
}

// AgentEndPayload closes one low-level agent run. WillRetry means another run
// will follow without settling. Error is the run failure, if any.
type AgentEndPayload struct {
	Messages  []Message `json:"messages,omitempty"`
	WillRetry bool      `json:"willRetry,omitempty"`
	Error     string    `json:"error,omitempty"`
	Aborted   bool      `json:"aborted,omitempty"`
}

// AgentSettledPayload reports that no automatic work remains.
type AgentSettledPayload struct {
	Aborted bool `json:"aborted"`
}

// TurnEndPayload closes one turn. Message is the final assistant message.
type TurnEndPayload struct {
	Message *Message `json:"message,omitempty"`
}

// MessagePayload is used by message_start and message_end. message_end is
// authoritative; message_start may carry an empty shell.
type MessagePayload struct {
	Message Message `json:"message"`
}

// MessageUpdatePayload carries only a delta. Transports must not grow partial
// message snapshots here; clients rebuild from deltas and replace with the
// message_end message.
type MessageUpdatePayload struct {
	MessageID    string `json:"messageId,omitempty"`
	Role         string `json:"role,omitempty"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
}

// ToolExecutionStartPayload announces a tool call.
type ToolExecutionStartPayload struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// ToolExecutionUpdatePayload reports a partial tool result.
type ToolExecutionUpdatePayload struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	PartialResult json.RawMessage `json:"partialResult,omitempty"`
}

// ToolExecutionEndPayload reports the final tool result.
type ToolExecutionEndPayload struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
}

// QueueUpdatePayload carries the complete current queues after every change.
type QueueUpdatePayload struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

// EntryAppendedPayload announces a new transcript entry.
type EntryAppendedPayload struct {
	Entry Entry `json:"entry"`
}

// CompactionStartPayload reports why compaction began.
type CompactionStartPayload struct {
	Reason string `json:"reason,omitempty"`
}

// CompactionEndPayload reports a successful compaction.
type CompactionEndPayload struct {
	Summary string `json:"summary,omitempty"`
}

// CompactionFailedPayload reports a failed compaction.
type CompactionFailedPayload struct {
	Error string `json:"error,omitempty"`
}

// RetryStartPayload announces an automatic retry.
type RetryStartPayload struct {
	Attempt int    `json:"attempt"`
	DelayMs int64  `json:"delayMs,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RetryEndPayload closes a retry attempt.
type RetryEndPayload struct {
	Attempt int    `json:"attempt"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ApprovalRequestPayload asks the client for a decision.
type ApprovalRequestPayload struct {
	ID        string          `json:"id"`
	ToolName  string          `json:"toolName,omitempty"`
	Title     string          `json:"title,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ApprovalResponsePayload reports a client decision.
type ApprovalResponsePayload struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Scope    string `json:"scope,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ModelChangedPayload reports a model switch.
type ModelChangedPayload struct {
	Model string `json:"model"`
}

// ThinkingLevelChangedPayload reports a thinking-level switch.
type ThinkingLevelChangedPayload struct {
	Level string `json:"level"`
}

// EncodeEvent marshals an event to JSON. The result contains no trailing
// newline; JSONL framing is the transport's job.
func EncodeEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// DecodeEvent decodes one JSON event record.
func DecodeEvent(data []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, err
	}
	return e, nil
}
