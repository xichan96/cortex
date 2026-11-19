package model

import (
	"context"
	"errors"
	"fmt"
)

// StreamEventType discriminates StreamEvent values. A well-formed response is:
//
//	start
//	(thinking_start thinking_delta* thinking_end)?
//	(text_start text_delta* text_end)?
//	(toolcall_start toolcall_delta* toolcall_end)*
//	(done | error)
//
// Deltas are the only incremental payload; end/done carry authoritative
// content. A failed stream always ends with an error event whose Message has
// StopReason error or aborted; Stream.Result never returns a transport error
// after streaming started.
type StreamEventType string

const (
	StreamStart         StreamEventType = "start"
	StreamTextStart     StreamEventType = "text_start"
	StreamTextDelta     StreamEventType = "text_delta"
	StreamTextEnd       StreamEventType = "text_end"
	StreamThinkingStart StreamEventType = "thinking_start"
	StreamThinkingDelta StreamEventType = "thinking_delta"
	StreamThinkingEnd   StreamEventType = "thinking_end"
	StreamToolStart     StreamEventType = "toolcall_start"
	StreamToolDelta     StreamEventType = "toolcall_delta"
	StreamToolEnd       StreamEventType = "toolcall_end"
	StreamDone          StreamEventType = "done"
	StreamError         StreamEventType = "error"
)

// IsTerminal reports whether t ends the event sequence.
func (t StreamEventType) IsTerminal() bool {
	return t == StreamDone || t == StreamError
}

// StreamErrorInfo is the structured payload of an error event.
type StreamErrorInfo struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	Cause      error  `json:"-"`
}

// Error implements error.
func (e *StreamErrorInfo) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

// Unwrap exposes the underlying transport error, if any.
func (e *StreamErrorInfo) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// AsStreamError extracts a *StreamErrorInfo from err, wrapping plain errors when
// needed. It returns nil for a nil error.
func AsStreamError(err error) *StreamErrorInfo {
	if err == nil {
		return nil
	}
	var streamErr *StreamErrorInfo
	if errors.As(err, &streamErr) {
		return streamErr
	}
	return &StreamErrorInfo{Message: err.Error(), Cause: err}
}

// StreamEvent is the single event type emitted by every provider. Fields are
// populated according to Type:
//
//	start                             Partial (optional)
//	text_start|text_delta|text_end    Index, Delta (delta), Partial (optional)
//	thinking_*                        Index, Delta (delta), Partial (optional)
//	toolcall_start|delta|end          Index, ToolCall (delta/end), Partial (optional)
//	done                              Message (authoritative), Usage, StopReason
//	error                             Message (authoritative), Err, StopReason
type StreamEvent struct {
	Type       StreamEventType   `json:"type"`
	Index      int               `json:"index,omitempty"`
	Delta      string            `json:"delta,omitempty"`
	ToolCall   *ToolCallDelta    `json:"toolCall,omitempty"`
	Partial    *AssistantMessage `json:"partial,omitempty"`
	Message    *AssistantMessage `json:"message,omitempty"`
	Usage      *Usage            `json:"usage,omitempty"`
	StopReason StopReason        `json:"stopReason,omitempty"`
	Err        *StreamErrorInfo  `json:"error,omitempty"`
}

// Terminal reports whether e ends the sequence.
func (e StreamEvent) Terminal() bool {
	return e.Type.IsTerminal()
}

// DoneEvent builds an authoritative terminal success event.
func DoneEvent(message *AssistantMessage) StreamEvent {
	event := StreamEvent{
		Type:       StreamDone,
		Message:    message,
		StopReason: StopReasonStop,
	}
	if message != nil {
		event.StopReason = message.StopReason
		event.Usage = &message.Usage
	}
	return event
}

// ErrorEvent builds an authoritative terminal error event. reason is coerced
// to error when it is not aborted/error, and to aborted when err represents a
// context cancellation.
func ErrorEvent(reason StopReason, err *StreamErrorInfo) StreamEvent {
	if reason != StopReasonAborted && reason != StopReasonError {
		reason = StopReasonError
	}
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		reason = StopReasonAborted
	}
	message := &AssistantMessage{
		Role:       RoleAssistant,
		StopReason: reason,
		Content:    []ContentBlock{},
	}
	if err != nil {
		message.ErrorMessage = err.Error()
	}
	return StreamEvent{
		Type:       StreamError,
		Message:    message,
		StopReason: reason,
		Err:        err,
	}
}

// ValidateSequence checks the basic ordering of a completed event slice. It is
// intended for provider tests and adapters; it does not validate provider
// specific semantics.
func ValidateSequence(events []StreamEvent) error {
	started := false
	terminal := false
	openText := false
	openThinking := false
	openTools := map[int]bool{}
	for i, event := range events {
		if terminal {
			return fmt.Errorf("event %d (%s) follows terminal event", i, event.Type)
		}
		switch event.Type {
		case StreamStart:
			if started {
				return fmt.Errorf("event %d: duplicate start", i)
			}
			started = true
		case StreamTextStart:
			if !started {
				return fmt.Errorf("event %d: %s before start", i, event.Type)
			}
			if openText {
				return fmt.Errorf("event %d: text_start while text is open", i)
			}
			openText = true
		case StreamTextDelta:
			if !openText {
				return fmt.Errorf("event %d: text_delta without text_start", i)
			}
		case StreamTextEnd:
			if !openText {
				return fmt.Errorf("event %d: text_end without text_start", i)
			}
			openText = false
		case StreamThinkingStart:
			if !started {
				return fmt.Errorf("event %d: %s before start", i, event.Type)
			}
			if openThinking {
				return fmt.Errorf("event %d: thinking_start while thinking is open", i)
			}
			openThinking = true
		case StreamThinkingDelta:
			if !openThinking {
				return fmt.Errorf("event %d: thinking_delta without thinking_start", i)
			}
		case StreamThinkingEnd:
			if !openThinking {
				return fmt.Errorf("event %d: thinking_end without thinking_start", i)
			}
			openThinking = false
		case StreamToolStart:
			if !started {
				return fmt.Errorf("event %d: %s before start", i, event.Type)
			}
			index := toolEventIndex(event)
			if openTools[index] {
				return fmt.Errorf("event %d: toolcall_start for already open index %d", i, index)
			}
			openTools[index] = true
		case StreamToolDelta:
			if !openTools[toolEventIndex(event)] {
				return fmt.Errorf("event %d: toolcall_delta without toolcall_start", i)
			}
		case StreamToolEnd:
			index := toolEventIndex(event)
			if !openTools[index] {
				return fmt.Errorf("event %d: toolcall_end without toolcall_start", i)
			}
			delete(openTools, index)
		case StreamDone, StreamError:
			if !started {
				return fmt.Errorf("event %d: %s before start", i, event.Type)
			}
			if openText || openThinking || len(openTools) > 0 {
				return fmt.Errorf("event %d: %s with open content blocks (text=%v thinking=%v tools=%d)",
					i, event.Type, openText, openThinking, len(openTools))
			}
			terminal = true
		default:
			return fmt.Errorf("event %d: unknown event type %q", i, event.Type)
		}
	}
	if !started {
		return fmt.Errorf("stream has no start event")
	}
	if !terminal {
		return fmt.Errorf("stream has no terminal event")
	}
	return nil
}

func toolEventIndex(event StreamEvent) int {
	if event.ToolCall != nil && event.ToolCall.Index != 0 {
		return event.ToolCall.Index
	}
	return event.Index
}
