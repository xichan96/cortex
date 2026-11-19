package extensions

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Event identifies a lifecycle point an extension can observe or influence.
// The catalogue is closed for the P0 skeleton: handlers may only subscribe to
// one of the constants below.
type Event string

const (
	EventProjectTrust Event = "project_trust"

	EventSessionStart         Event = "session_start"
	EventSessionBeforeSwitch  Event = "session_before_switch"
	EventSessionBeforeFork    Event = "session_before_fork"
	EventSessionBeforeCompact Event = "session_before_compact"
	EventSessionCompact       Event = "session_compact"
	EventSessionShutdown      Event = "session_shutdown"

	EventAgentStart        Event = "agent_start"
	EventTurnStart         Event = "turn_start"
	EventTurnEnd           Event = "turn_end"
	EventAgentBeforeSettle Event = "agent_before_settle"
	EventAgentSettled      Event = "agent_settled"

	EventMessageStart  Event = "message_start"
	EventMessageUpdate Event = "message_update"
	EventMessageEnd    Event = "message_end"

	EventToolCall            Event = "tool_call"
	EventToolResult          Event = "tool_result"
	EventToolExecutionStart  Event = "tool_execution_start"
	EventToolExecutionUpdate Event = "tool_execution_update"
	EventToolExecutionEnd    Event = "tool_execution_end"

	EventContext               Event = "context"
	EventBeforeProviderRequest Event = "before_provider_request"
	EventAfterProviderResponse Event = "after_provider_response"
	EventInput                 Event = "input"
	EventUserBash              Event = "user_bash"
	EventModelSelect           Event = "model_select"
)

// String returns the wire name of the event.
func (e Event) String() string { return string(e) }

// EventClass describes how handler results are merged (design §7.3).
type EventClass string

const (
	// ClassNotification handlers observe only; results are ignored.
	ClassNotification EventClass = "notification"
	// ClassMutation handlers may replace or mutate the payload in place; later
	// handlers observe the change.
	ClassMutation EventClass = "mutation"
	// ClassTransform handlers chain: the previous output is the next input.
	ClassTransform EventClass = "transform"
	// ClassGate handlers may block; the first decisive result wins.
	ClassGate EventClass = "gate"
	// ClassBoundary handlers decide whether a boundary continues or ends; the
	// first decisive result wins.
	ClassBoundary EventClass = "boundary"
	// ClassLastOverride the last non-empty value wins.
	ClassLastOverride EventClass = "last_override"
)

// EventSpec is the metadata that drives dispatch for one event.
type EventSpec struct {
	// Classes are the merge semantics of the event. Most events have exactly
	// one; tool_call, tool_result, and input carry two.
	Classes []EventClass
	// FailClosed makes a handler error in a gate decisive: dispatch blocks.
	// tool_call sets this (design §7.3).
	FailClosed bool
}

// Spec returns a copy of the event's specification. Unknown events are treated
// as notifications so that Emit never fails on forward-compatible payloads.
func (e Event) Spec() EventSpec {
	spec, ok := eventSpecs[e]
	if !ok {
		return EventSpec{Classes: []EventClass{ClassNotification}}
	}
	spec.Classes = append([]EventClass(nil), spec.Classes...)
	return spec
}

// Classes returns the event's merge semantics.
func (e Event) Classes() []EventClass { return e.Spec().Classes }

// HasClass reports whether the event carries the given semantics.
func (e Event) HasClass(c EventClass) bool {
	for _, have := range e.Classes() {
		if have == c {
			return true
		}
	}
	return false
}

// IsKnown reports whether the event belongs to the catalogue.
func (e Event) IsKnown() bool {
	_, ok := eventSpecs[e]
	return ok
}

// KnownEvents returns the catalogue in alphabetical order.
func KnownEvents() []Event {
	out := make([]Event, 0, len(eventSpecs))
	for ev := range eventSpecs {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// eventSpecs classifies every catalogue event. Keep this table in sync with the
// design document (§7.2–§7.3).
var eventSpecs = map[Event]EventSpec{
	EventProjectTrust: {Classes: []EventClass{ClassGate}},

	EventSessionStart: {Classes: []EventClass{ClassNotification}},
	// Switching and forking are boundary points: a handler may veto or extend.
	EventSessionBeforeSwitch:  {Classes: []EventClass{ClassBoundary}},
	EventSessionBeforeFork:    {Classes: []EventClass{ClassBoundary}},
	EventSessionBeforeCompact: {Classes: []EventClass{ClassTransform, ClassBoundary}},
	EventSessionCompact:       {Classes: []EventClass{ClassNotification}},
	EventSessionShutdown:      {Classes: []EventClass{ClassNotification}},

	EventAgentStart:        {Classes: []EventClass{ClassNotification}},
	EventTurnStart:         {Classes: []EventClass{ClassNotification}},
	EventTurnEnd:           {Classes: []EventClass{ClassBoundary}},
	EventAgentBeforeSettle: {Classes: []EventClass{ClassBoundary}},
	EventAgentSettled:      {Classes: []EventClass{ClassNotification}},

	EventMessageStart:  {Classes: []EventClass{ClassNotification}},
	EventMessageUpdate: {Classes: []EventClass{ClassNotification}},
	EventMessageEnd:    {Classes: []EventClass{ClassTransform}},

	// tool_call can patch arguments (mutation) and block execution (gate). A
	// handler failure must not let the call through.
	EventToolCall:            {Classes: []EventClass{ClassMutation, ClassGate}, FailClosed: true},
	EventToolResult:          {Classes: []EventClass{ClassMutation, ClassTransform}},
	EventToolExecutionStart:  {Classes: []EventClass{ClassNotification}},
	EventToolExecutionUpdate: {Classes: []EventClass{ClassNotification}},
	EventToolExecutionEnd:    {Classes: []EventClass{ClassNotification}},

	EventContext:               {Classes: []EventClass{ClassTransform}},
	EventBeforeProviderRequest: {Classes: []EventClass{ClassTransform}},
	EventAfterProviderResponse: {Classes: []EventClass{ClassNotification}},
	EventInput:                 {Classes: []EventClass{ClassTransform, ClassGate}},
	EventUserBash:              {Classes: []EventClass{ClassGate}},
	EventModelSelect:           {Classes: []EventClass{ClassLastOverride}},
}

// EventEnvelope carries an event and its correlation identifiers (design §7.3).
// The same envelope pointer is passed to every handler of one Emit call, so an
// in-place mutation of Payload is visible to later handlers. Payload may also be
// replaced through EventResult.Value.
type EventEnvelope struct {
	Event Event
	// SessionID and TurnID correlate the event with a session and turn.
	SessionID string
	TurnID    string
	// ToolCallID is set for tool events.
	ToolCallID string
	// Source identifies the emitter (host, extension, or plugin).
	Source SourceInfo
	// Timestamp is set by the emitter; the dispatcher does not fill it.
	Timestamp time.Time
	// Payload is the event-specific object. Its concrete type is defined by the
	// emitting host; extensions type-assert it.
	Payload any
}

// Decision is a handler's verdict for gate and boundary events.
type Decision string

const (
	// DecisionNone means the handler expresses no verdict.
	DecisionNone Decision = ""
	// DecisionContinue asks a boundary to continue instead of settling.
	DecisionContinue Decision = "continue"
	// DecisionBlock vetoes a gate (for example a tool call).
	DecisionBlock Decision = "block"
	// DecisionEnd asks a boundary to end.
	DecisionEnd Decision = "end"
)

// EventResult is what a handler returns.
//
// Value has class-dependent meaning:
//   - mutation/transform: when non-nil, it replaces the envelope payload for
//     subsequent handlers (transform chaining);
//   - last-override: the last non-nil value wins;
//   - all other classes: ignored.
//
// Decision is honored by gate (block) and boundary (continue/end).
type EventResult struct {
	Decision Decision
	Reason   string
	Value    any
}

// Handler handles one event. Returning an error never aborts sibling handlers;
// the error is recorded in EmitResult.Errors and reported to the error reporter.
type Handler func(ctx context.Context, ev *EventEnvelope) (EventResult, error)

// HandlerError describes a handler failure. Errors are isolated: the dispatcher
// continues with the next handler (or blocks, for fail-closed gates).
type HandlerError struct {
	Event  Event
	Owner  string
	Source SourceInfo
	Err    error
}

// Error implements error.
func (e HandlerError) Error() string {
	who := e.Owner
	if who == "" {
		who = e.Source.String()
	}
	return fmt.Sprintf("extensions: %s handler (%s) failed: %v", e.Event, who, e.Err)
}

// Unwrap exposes the underlying error.
func (e HandlerError) Unwrap() error { return e.Err }

// EmitResult is the merged outcome of one dispatch.
type EmitResult struct {
	// Decision is the first decisive gate/boundary verdict, if any.
	Decision Decision
	// Reason accompanies Decision when the handler supplied one.
	Reason string
	// Value is the final mutated/transformed/overridden payload when a handler
	// produced one; nil means no handler changed the payload.
	Value any
	// Handled is the number of handlers invoked (including those that errored).
	Handled int
	// Errors contains every isolated handler failure, in execution order.
	Errors []HandlerError
}

// Blocked reports whether a gate vetoed the event or dispatch failed closed.
func (r EmitResult) Blocked() bool { return r.Decision == DecisionBlock }
