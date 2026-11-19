package dino

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/extensions"
	"github.com/xichan96/cortex/dino/session"
	"github.com/xichan96/cortex/pkg/logger"
)

// P6-C: in-process extension event bridge.
//
// The sink below turns dino session notifications into extensions.EventEnvelope
// dispatches. It is deliberately notification-only: it never gates a tool call
// or rewrites message content, and registry handler failures are isolated by
// the dispatcher (returned in EmitResult.Errors), so an extension can never
// break a session.
//
// Telemetry safety: payloads carry identifiers, tool names, status and byte
// counts only. Prompt text, assistant output and tool arguments/results never
// leave the session through this bridge.

// extensionEventSource identifies dino as the emitter of bridged events.
var extensionEventSource = extensions.SourceInfo{Kind: extensions.SourceBuiltin, ID: "dino-session"}

// ExtensionSessionPayload is the payload of session_start/session_shutdown.
type ExtensionSessionPayload struct {
	// Reason is "created" for session_start and "closed" for session_shutdown.
	Reason string `json:"reason,omitempty"`
}

// ExtensionTurnPayload is the payload of turn_start/turn_end.
type ExtensionTurnPayload struct {
	// Source is the turn origin: "user" or "subagent".
	Source string `json:"source,omitempty"`
}

// ExtensionMessagePayload is the payload of message_start/message_update/
// message_end. DeltaBytes is the length of one streamed fragment; the fragment
// itself is intentionally not included.
type ExtensionMessagePayload struct {
	// Role is "user" or "assistant".
	Role string `json:"role,omitempty"`
	// Kind is "text" or "thinking".
	Kind string `json:"kind,omitempty"`
	// DeltaBytes is the byte length of this update's fragment (0 on start/end).
	DeltaBytes int `json:"delta_bytes,omitempty"`
}

// ExtensionToolPayload is the payload of tool_execution_start/update/end.
// Tool inputs and outputs are never included; correlate with ToolCallID and
// ToolName instead.
type ExtensionToolPayload struct {
	ToolName string `json:"tool_name,omitempty"`
	// Status is "start", "update", "end" or "error".
	Status string `json:"status,omitempty"`
}

// extensionEventSink implements session.Observer and bridges session events to
// the extension registry. The zero value is not usable; use
// newExtensionEventSink.
type extensionEventSink struct {
	registry  *extensions.Registry
	sessionID string
	// subscriptionID is the session observer id returned by Subscribe; it is
	// used by the factory to unsubscribe on CloseSession/CloseAll.
	subscriptionID string

	mu sync.Mutex
	// turnID is empty while the session is idle. Dino does not expose turn
	// identifiers, so the sink mints a per-session monotonic one.
	turnID  string
	turnSeq int
	// messageOpen tracks the streaming message currently being aggregated.
	messageOpen bool
	messageRole string
	messageKind string
}

func newExtensionEventSink(registry *extensions.Registry, sessionID string) *extensionEventSink {
	return &extensionEventSink{
		registry:  registry,
		sessionID: sessionID,
	}
}

// OnEvent maps one dino session event onto zero or more extension events.
func (s *extensionEventSink) OnEvent(ev *session.Event) {
	if s == nil || s.registry == nil || ev == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	switch ev.Type {
	case session.EventTypeMessage:
		// The session echoes the user turn input (and subagent wake input) as a
		// message event carrying EventSourceUser/EventSourceSubagent; model
		// output chunks leave Source empty. Use that to tell them apart.
		if ev.Source == session.EventSourceUser || ev.Source == session.EventSourceSubagent {
			s.startUserTurnLocked(ev.Source)
			return
		}
		s.appendMessageLocked("assistant", "text", len(ev.Content))
	case session.EventTypeThinking:
		s.appendMessageLocked("assistant", "thinking", len(ev.Content))
	case session.EventTypeToolCall, session.EventTypeToolStart:
		s.startToolLocked(ev)
	case session.EventTypeToolInputDelta:
		s.updateToolLocked(ev)
	case session.EventTypeToolResult:
		s.endToolLocked(ev, "end")
	case session.EventTypeError:
		if ev.ToolCallID != "" {
			s.endToolLocked(ev, "error")
			return
		}
		// Non-tool errors abort the turn in session.executeWithInput; close the
		// open message but wait for Done (or the next turn) to end the turn so
		// recoverable errors do not create synthetic turn boundaries.
		s.endMessageLocked()
	case session.EventTypeDone:
		s.endMessageLocked()
		s.endTurnLocked()
	}
}

// emitSessionStart announces session creation. Called by the factory right
// after subscribing the sink.
func (s *extensionEventSink) emitSessionStart() {
	if s == nil || s.registry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(extensions.EventSessionStart, "", ExtensionSessionPayload{Reason: "created"})
}

// emitSessionShutdown flushes an in-flight message/turn and announces the
// session's end. Called by the factory on CloseSession/CloseAll.
func (s *extensionEventSink) emitSessionShutdown() {
	if s == nil || s.registry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endMessageLocked()
	s.endTurnLocked()
	s.emitLocked(extensions.EventSessionShutdown, "", ExtensionSessionPayload{Reason: "closed"})
}

func (s *extensionEventSink) startUserTurnLocked(source session.EventSource) {
	// Defensive: if a previous turn never saw Done (provider/stream failure),
	// close it before opening a new one so turn_start/end stay balanced.
	s.endMessageLocked()
	s.endTurnLocked()

	s.turnSeq++
	s.turnID = fmt.Sprintf("turn-%d", s.turnSeq)
	origin := string(source)
	if origin == "" {
		origin = string(session.EventSourceUser)
	}
	s.emitLocked(extensions.EventTurnStart, "", ExtensionTurnPayload{Source: origin})
	// The user echo is a single complete event, not a stream.
	payload := ExtensionMessagePayload{Role: "user", Kind: "text"}
	s.emitLocked(extensions.EventMessageStart, "", payload)
	s.emitLocked(extensions.EventMessageEnd, "", payload)
}

func (s *extensionEventSink) appendMessageLocked(role, kind string, deltaBytes int) {
	if s.turnID == "" {
		// Events should always follow a user turn echo; start one defensively
		// (for example a subagent-driven or resumed stream) rather than
		// dropping the notification.
		s.startUserTurnLocked(session.EventSourceSubagent)
	}
	if !s.messageOpen || s.messageRole != role || s.messageKind != kind {
		s.endMessageLocked()
		s.messageOpen = true
		s.messageRole = role
		s.messageKind = kind
		s.emitLocked(extensions.EventMessageStart, "", ExtensionMessagePayload{Role: role, Kind: kind})
	}
	s.emitLocked(extensions.EventMessageUpdate, "", ExtensionMessagePayload{
		Role:       role,
		Kind:       kind,
		DeltaBytes: deltaBytes,
	})
}

func (s *extensionEventSink) endMessageLocked() {
	if !s.messageOpen {
		return
	}
	s.messageOpen = false
	s.emitLocked(extensions.EventMessageEnd, "", ExtensionMessagePayload{
		Role: s.messageRole,
		Kind: s.messageKind,
	})
	s.messageRole = ""
	s.messageKind = ""
}

func (s *extensionEventSink) startToolLocked(ev *session.Event) {
	if s.turnID == "" {
		s.startUserTurnLocked(session.EventSourceSubagent)
	}
	s.emitLocked(extensions.EventToolExecutionStart, ev.ToolCallID, ExtensionToolPayload{
		ToolName: ev.ToolName,
		Status:   "start",
	})
}

func (s *extensionEventSink) updateToolLocked(ev *session.Event) {
	s.emitLocked(extensions.EventToolExecutionUpdate, ev.ToolCallID, ExtensionToolPayload{
		ToolName: ev.ToolName,
		Status:   "update",
	})
}

func (s *extensionEventSink) endToolLocked(ev *session.Event, status string) {
	s.emitLocked(extensions.EventToolExecutionEnd, ev.ToolCallID, ExtensionToolPayload{
		ToolName: ev.ToolName,
		Status:   status,
	})
}

func (s *extensionEventSink) endTurnLocked() {
	if s.turnID == "" {
		return
	}
	s.emitLocked(extensions.EventTurnEnd, "", ExtensionTurnPayload{})
	s.turnID = ""
}

// emitLocked builds an envelope and dispatches it through the registry. The
// returned errors are handler failures; they are logged and never propagated,
// so extension code cannot affect the session loop.
func (s *extensionEventSink) emitLocked(event extensions.Event, toolCallID string, payload any) {
	env := &extensions.EventEnvelope{
		Event:      event,
		SessionID:  s.sessionID,
		TurnID:     s.turnID,
		ToolCallID: toolCallID,
		Source:     extensionEventSource,
		Timestamp:  time.Now(),
		Payload:    payload,
	}
	result := s.registry.Emit(context.Background(), env)
	for _, handlerErr := range result.Errors {
		logger.Warn("[DinoFactory] extension event handler failed",
			slog.String("event", string(event)),
			slog.String("session_id", s.sessionID),
			slog.String("owner", handlerErr.Owner),
			slog.String("error", handlerErr.Error()))
	}
}

// attachExtensionEvents subscribes a sink to sess and emits session_start. It
// is a no-op unless WithExtensionRegistry was used. Called from CreateSession
// while f.mu is held: handlers must be non-blocking (they are trusted
// in-process extension code and do not receive the factory).
func (f *dinoFactory) attachExtensionEvents(sess *session.Session, sessionID string) {
	if f.extensionRegistry == nil || sess == nil {
		return
	}
	if _, exists := f.extensionEventSinks[sessionID]; exists {
		return
	}
	sink := newExtensionEventSink(f.extensionRegistry, sessionID)
	sink.subscriptionID = sess.Subscribe(sink)
	f.extensionEventSinks[sessionID] = sink
	sink.emitSessionStart()
}

// detachExtensionEvents unsubscribes the sink and emits session_shutdown.
// Called from CloseSession/CloseAll while f.mu is held.
func (f *dinoFactory) detachExtensionEvents(sessionID string, sess *session.Session) {
	sink, exists := f.extensionEventSinks[sessionID]
	if !exists {
		return
	}
	delete(f.extensionEventSinks, sessionID)
	if sess != nil {
		sess.Unsubscribe(sink.subscriptionID)
	}
	sink.emitSessionShutdown()
}
