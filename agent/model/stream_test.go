package model

import (
	"context"
	"errors"
	"testing"
)

func validEventSequence() []StreamEvent {
	return []StreamEvent{
		{Type: StreamStart},
		{Type: StreamThinkingStart, Index: 0},
		{Type: StreamThinkingDelta, Index: 0, Delta: "plan"},
		{Type: StreamThinkingEnd, Index: 0},
		{Type: StreamTextStart, Index: 1},
		{Type: StreamTextDelta, Index: 1, Delta: "hello"},
		{Type: StreamTextEnd, Index: 1, Delta: "hello"},
		{Type: StreamToolStart, Index: 2, ToolCall: &ToolCallDelta{Index: 2, ID: "call_1", Name: "read"}},
		{Type: StreamToolDelta, Index: 2, ToolCall: &ToolCallDelta{Index: 2, ArgumentsDelta: `{"pa`}},
		{Type: StreamToolEnd, Index: 2, ToolCall: &ToolCallDelta{Index: 2, ID: "call_1", Name: "read", Arguments: map[string]any{"path": "x"}}},
		DoneEvent(&AssistantMessage{StopReason: StopReasonToolUse}),
	}
}

func TestValidateSequenceAcceptsDocumentedSequence(t *testing.T) {
	if err := ValidateSequence(validEventSequence()); err != nil {
		t.Fatalf("valid sequence rejected: %v", err)
	}
}

func TestValidateSequenceAcceptsInterleavedToolCalls(t *testing.T) {
	events := []StreamEvent{
		{Type: StreamStart},
		{Type: StreamToolStart, Index: 1, ToolCall: &ToolCallDelta{Index: 1, ID: "a", Name: "read"}},
		{Type: StreamToolStart, Index: 2, ToolCall: &ToolCallDelta{Index: 2, ID: "b", Name: "write"}},
		{Type: StreamToolDelta, Index: 2, ToolCall: &ToolCallDelta{Index: 2, ArgumentsDelta: `{"x":1}`}},
		{Type: StreamToolEnd, Index: 2, ToolCall: &ToolCallDelta{Index: 2, Arguments: map[string]any{"x": 1}}},
		{Type: StreamToolDelta, Index: 1, ToolCall: &ToolCallDelta{Index: 1, ArgumentsDelta: `{}`}},
		{Type: StreamToolEnd, Index: 1, ToolCall: &ToolCallDelta{Index: 1, Arguments: map[string]any{}}},
		DoneEvent(&AssistantMessage{StopReason: StopReasonToolUse}),
	}
	if err := ValidateSequence(events); err != nil {
		t.Fatalf("interleaved tool calls rejected: %v", err)
	}
}

func TestValidateSequenceRejectsInvalidOrdering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]StreamEvent) []StreamEvent
	}{
		{"no start", func(events []StreamEvent) []StreamEvent { return events[1:] }},
		{"no terminal", func(events []StreamEvent) []StreamEvent { return events[:len(events)-1] }},
		{"delta without start", func(events []StreamEvent) []StreamEvent {
			return append(events[:4], events[5:]...)
		}},
		{"event after terminal", func(events []StreamEvent) []StreamEvent {
			return append(events, StreamEvent{Type: StreamTextDelta, Index: 1})
		}},
		{"tool delta without start", func(events []StreamEvent) []StreamEvent {
			return append(events[:7], events[8:]...)
		}},
		{"unknown type", func(events []StreamEvent) []StreamEvent {
			events[1].Type = "bogus"
			return events
		}},
		{"done with open text", func(events []StreamEvent) []StreamEvent {
			return []StreamEvent{
				{Type: StreamStart},
				{Type: StreamTextStart, Index: 0},
				DoneEvent(&AssistantMessage{StopReason: StopReasonStop}),
			}
		}},
		{"done with open tool call", func(events []StreamEvent) []StreamEvent {
			return []StreamEvent{
				{Type: StreamStart},
				{Type: StreamToolStart, Index: 1, ToolCall: &ToolCallDelta{Index: 1, ID: "a"}},
				DoneEvent(&AssistantMessage{StopReason: StopReasonToolUse}),
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSequence(tt.mutate(validEventSequence())); err == nil {
				t.Fatal("expected sequence validation error")
			}
		})
	}
}

func TestDoneEventCarriesAuthoritativeMessage(t *testing.T) {
	message := &AssistantMessage{
		Content:    []ContentBlock{{Type: ContentText, Text: "hello"}},
		StopReason: StopReasonStop,
		Usage:      Usage{Input: 10, Output: 2, TotalTokens: 12},
	}
	event := DoneEvent(message)
	if event.Type != StreamDone || event.Message != message {
		t.Fatalf("unexpected done event: %+v", event)
	}
	if event.StopReason != StopReasonStop {
		t.Fatalf("stop reason = %q", event.StopReason)
	}
	if event.Usage == nil || event.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", event.Usage)
	}
	if !event.Terminal() {
		t.Fatal("done event should be terminal")
	}
}

func TestErrorEvent(t *testing.T) {
	event := ErrorEvent(StopReasonStop, &StreamErrorInfo{Code: "bad_request", Message: "invalid"})
	if event.Type != StreamError || event.StopReason != StopReasonError {
		t.Fatalf("non-error reason was not coerced: %+v", event)
	}
	if event.Message == nil || event.Message.StopReason != StopReasonError || event.Message.ErrorMessage == "" {
		t.Fatalf("missing authoritative error message: %+v", event.Message)
	}
	if !event.Terminal() {
		t.Fatal("error event should be terminal")
	}

	canceled := ErrorEvent(StopReasonError, &StreamErrorInfo{
		Message: "aborted",
		Cause:   context.Canceled,
	})
	if canceled.StopReason != StopReasonAborted || canceled.Message.StopReason != StopReasonAborted {
		t.Fatalf("context cancellation should map to aborted: %+v", canceled)
	}
}

func TestAsStreamError(t *testing.T) {
	if AsStreamError(nil) != nil {
		t.Fatal("nil error should produce nil")
	}
	original := &StreamErrorInfo{Code: "overloaded", Message: "busy"}
	if got := AsStreamError(original); got != original {
		t.Fatalf("AsStreamError returned a different value: %+v", got)
	}
	wrapped := errors.New("connection reset")
	got := AsStreamError(wrapped)
	if got == nil || got.Message != "connection reset" || !errors.Is(got, wrapped) {
		t.Fatalf("plain error not wrapped: %+v", got)
	}
}

func TestUsageAdd(t *testing.T) {
	var total Usage
	total.Add(Usage{
		Input: 10, Output: 2, CacheRead: 5, Reasoning: 1, TotalTokens: 12,
		Cost: UsageCost{Input: 0.1, Total: 0.2},
	})
	total.Add(Usage{
		Input: 3, Output: 1, TotalTokens: 4,
		Cost: UsageCost{Output: 0.05, Total: 0.05, TotalFromProvider: true},
	})
	if total.Input != 13 || total.Output != 3 || total.CacheRead != 5 ||
		total.Reasoning != 1 || total.TotalTokens != 16 {
		t.Fatalf("unexpected token totals: %+v", total)
	}
	if !approxEqual(total.Cost.Total, 0.25) || !total.Cost.TotalFromProvider {
		t.Fatalf("unexpected cost totals: %+v", total.Cost)
	}
}
