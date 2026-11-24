package dino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/extensions"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/session"
)

const extensionEventsProviderName = "mock-extension-events"

// registerExtensionEventsProvider installs a streaming mock that emits a proper
// "chunk" so the session produces assistant message events.
func registerExtensionEventsProvider(t *testing.T, response string) {
	t.Helper()
	RegisterLLMProvider(extensionEventsProviderName, func(*Config) (types.LLMProvider, error) {
		return &transcriptLiveProvider{response: response}, nil
	})
}

// extensionEventRecorder subscribes to registry events and records counts and
// envelope copies for assertions.
type extensionEventRecorder struct {
	mu        sync.Mutex
	counts    map[extensions.Event]int
	envelopes []extensions.EventEnvelope
	onEvent   map[extensions.Event]func()
}

func newExtensionEventRecorder(reg *extensions.Registry, events []extensions.Event) (*extensionEventRecorder, error) {
	rec := &extensionEventRecorder{
		counts:  make(map[extensions.Event]int, len(events)),
		onEvent: make(map[extensions.Event]func()),
	}
	for _, event := range events {
		event := event
		if _, err := reg.Events().On(event, func(_ context.Context, env *extensions.EventEnvelope) (extensions.EventResult, error) {
			rec.mu.Lock()
			rec.counts[event]++
			rec.envelopes = append(rec.envelopes, *env)
			hook := rec.onEvent[event]
			rec.mu.Unlock()
			if hook != nil {
				hook()
			}
			return extensions.EventResult{}, nil
		}); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

func (r *extensionEventRecorder) setHook(event extensions.Event, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onEvent[event] = fn
}

func (r *extensionEventRecorder) count(event extensions.Event) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[event]
}

func (r *extensionEventRecorder) snapshot() []extensions.EventEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]extensions.EventEnvelope(nil), r.envelopes...)
}

func TestExtensionEventsDispatchSessionLifecycle(t *testing.T) {
	const (
		sessionID     = "ext-events-session"
		prompt        = "prompt-that-must-not-leak-8123"
		assistantText = "assistant-reply-that-must-not-leak"
	)
	registerExtensionEventsProvider(t, assistantText)

	reg := extensions.NewRegistry()
	rec, err := newExtensionEventRecorder(reg, []extensions.Event{
		extensions.EventSessionStart,
		extensions.EventTurnStart,
		extensions.EventTurnEnd,
		extensions.EventMessageStart,
		extensions.EventMessageUpdate,
		extensions.EventMessageEnd,
		extensions.EventSessionShutdown,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	turnEnded := make(chan struct{})
	var once sync.Once
	rec.setHook(extensions.EventTurnEnd, func() { once.Do(func() { close(turnEnded) }) })

	cfg := getTestConfig()
	cfg.Provider.Type = extensionEventsProviderName
	factory, err := NewDinoFactory(cfg, WithExtensionRegistry(reg))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	sess, err := factory.CreateSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := rec.count(extensions.EventSessionStart); got != 1 {
		t.Fatalf("session_start count after CreateSession = %d, want 1", got)
	}

	sess.Input() <- prompt
	select {
	case <-turnEnded:
	case <-time.After(10 * time.Second):
		t.Fatal("turn_end extension event not received")
	}

	if got := rec.count(extensions.EventTurnStart); got != 1 {
		t.Errorf("turn_start count = %d, want 1", got)
	}
	if got := rec.count(extensions.EventTurnEnd); got != 1 {
		t.Errorf("turn_end count = %d, want 1", got)
	}
	if got := rec.count(extensions.EventMessageStart); got < 2 {
		t.Errorf("message_start count = %d, want at least 2 (user + assistant)", got)
	}
	if got := rec.count(extensions.EventMessageUpdate); got < 1 {
		t.Errorf("message_update count = %d, want at least 1", got)
	}
	if got := rec.count(extensions.EventMessageEnd); got < 2 {
		t.Errorf("message_end count = %d, want at least 2 (user + assistant)", got)
	}

	factory.CloseSession(sessionID)
	if got := rec.count(extensions.EventSessionShutdown); got != 1 {
		t.Fatalf("session_shutdown count after CloseSession = %d, want 1", got)
	}

	// Correlation ids and telemetry safety: every envelope carries the session
	// id, turn events carry a turn id, and no payload embeds prompt or output.
	for _, env := range rec.snapshot() {
		if env.SessionID != sessionID {
			t.Errorf("%s envelope session id = %q, want %q", env.Event, env.SessionID, sessionID)
		}
		switch env.Event {
		case extensions.EventTurnStart, extensions.EventTurnEnd:
			if env.TurnID == "" {
				t.Errorf("%s envelope has empty turn id", env.Event)
			}
		}
		payload, err := json.Marshal(env.Payload)
		if err != nil {
			t.Fatalf("marshal %s payload: %v", env.Event, err)
		}
		if strings.Contains(string(payload), prompt) {
			t.Errorf("%s payload leaked prompt text: %s", env.Event, payload)
		}
		if strings.Contains(string(payload), assistantText) {
			t.Errorf("%s payload leaked model output: %s", env.Event, payload)
		}
	}
}

func TestExtensionEventsHandlerErrorsAreIsolated(t *testing.T) {
	const sessionID = "ext-events-errors"
	registerExtensionEventsProvider(t, "ok")

	reg := extensions.NewRegistry()
	failing := func(context.Context, *extensions.EventEnvelope) (extensions.EventResult, error) {
		return extensions.EventResult{}, errors.New("boom")
	}
	for _, event := range []extensions.Event{extensions.EventSessionStart, extensions.EventTurnStart, extensions.EventMessageEnd} {
		if _, err := reg.Events().On(event, failing); err != nil {
			t.Fatalf("subscribe %s: %v", event, err)
		}
	}

	turnEnded := make(chan struct{})
	var once sync.Once
	if _, err := reg.Events().On(extensions.EventTurnEnd, func(context.Context, *extensions.EventEnvelope) (extensions.EventResult, error) {
		once.Do(func() { close(turnEnded) })
		return extensions.EventResult{}, nil
	}); err != nil {
		t.Fatalf("subscribe turn_end: %v", err)
	}

	cfg := getTestConfig()
	cfg.Provider.Type = extensionEventsProviderName
	factory, err := NewDinoFactory(cfg, WithExtensionRegistry(reg))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	sess, err := factory.CreateSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("CreateSession must succeed despite failing session_start handler: %v", err)
	}
	sess.Input() <- "hello"
	select {
	case <-turnEnded:
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not complete despite extension handler errors")
	}
	factory.CloseSession(sessionID)
}

func TestExtensionEventsCloseAllEmitsShutdown(t *testing.T) {
	registerExtensionEventsProvider(t, "ok")

	reg := extensions.NewRegistry()
	var mu sync.Mutex
	shutdowns := 0
	if _, err := reg.Events().On(extensions.EventSessionShutdown, func(context.Context, *extensions.EventEnvelope) (extensions.EventResult, error) {
		mu.Lock()
		defer mu.Unlock()
		shutdowns++
		return extensions.EventResult{}, nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	cfg := getTestConfig()
	cfg.Provider.Type = extensionEventsProviderName
	factory, err := NewDinoFactory(cfg, WithExtensionRegistry(reg))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	for _, id := range []string{"ext-events-a", "ext-events-b"} {
		if _, err := factory.CreateSession(ctx, id); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	factory.CloseAll()

	mu.Lock()
	defer mu.Unlock()
	if shutdowns != 2 {
		t.Fatalf("session_shutdown count after CloseAll = %d, want 2", shutdowns)
	}
}

// Ensure the sink maps tool session events to tool_execution_* notifications.
// Driven directly (no provider round trip) to keep the mapping unit-focused.
func TestExtensionEventSinkMapsToolEvents(t *testing.T) {
	reg := extensions.NewRegistry()
	rec, err := newExtensionEventRecorder(reg, []extensions.Event{
		extensions.EventToolExecutionStart,
		extensions.EventToolExecutionUpdate,
		extensions.EventToolExecutionEnd,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	sink := newExtensionEventSink(reg, "ext-events-tools")
	sink.emitSessionStart()
	sink.OnEvent(&session.Event{Type: session.EventTypeToolCall, ToolName: "Read", ToolCallID: "call-1", ToolInput: map[string]any{"path": "/secret"}})
	sink.OnEvent(&session.Event{Type: session.EventTypeToolInputDelta, ToolName: "Read", ToolCallID: "call-1", Content: "secret-args"})
	sink.OnEvent(&session.Event{Type: session.EventTypeToolResult, ToolName: "Read", ToolCallID: "call-1", ToolOutput: "secret-output"})

	if got := rec.count(extensions.EventToolExecutionStart); got != 1 {
		t.Errorf("tool_execution_start = %d, want 1", got)
	}
	if got := rec.count(extensions.EventToolExecutionUpdate); got != 1 {
		t.Errorf("tool_execution_update = %d, want 1", got)
	}
	if got := rec.count(extensions.EventToolExecutionEnd); got != 1 {
		t.Errorf("tool_execution_end = %d, want 1", got)
	}

	for _, env := range rec.snapshot() {
		if env.ToolCallID != "call-1" {
			t.Errorf("%s tool call id = %q, want call-1", env.Event, env.ToolCallID)
		}
		payload, err := json.Marshal(env.Payload)
		if err != nil {
			t.Fatalf("marshal %s payload: %v", env.Event, err)
		}
		for _, leaked := range []string{"/secret", "secret-args", "secret-output"} {
			if strings.Contains(string(payload), leaked) {
				t.Errorf("%s payload leaked tool data %q: %s", env.Event, leaked, payload)
			}
		}
	}
}
