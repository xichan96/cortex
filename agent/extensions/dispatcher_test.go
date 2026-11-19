package extensions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDispatcherRunsHandlersInRegistrationOrder(t *testing.T) {
	d := NewDispatcher()
	var got []string
	for _, name := range []string{"a", "b", "c"} {
		name := name
		if _, err := d.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
			got = append(got, name)
			return EventResult{}, nil
		}); err != nil {
			t.Fatalf("subscribe %s: %v", name, err)
		}
	}

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventAgentStart})
	if result.Handled != 3 {
		t.Fatalf("Handled = %d, want 3", result.Handled)
	}
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v, want [a b c]", got)
	}
	if result.Decision != DecisionNone {
		t.Fatalf("Decision = %q, want none", result.Decision)
	}
}

func TestDispatcherMutationVisibleToLaterHandlers(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.On(EventToolCall, func(_ context.Context, env *EventEnvelope) (EventResult, error) {
		args := env.Payload.(map[string]any)
		args["patched"] = true
		return EventResult{}, nil
	})
	var observed any
	_, _ = d.On(EventToolCall, func(_ context.Context, env *EventEnvelope) (EventResult, error) {
		observed = env.Payload.(map[string]any)["patched"]
		return EventResult{}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{
		Event:   EventToolCall,
		Payload: map[string]any{"path": "x"},
	})
	if observed != true {
		t.Fatalf("second handler observed %v, want in-place mutation", observed)
	}
	if result.Blocked() {
		t.Fatal("event should not be blocked")
	}
}

func TestDispatcherMutationByValueReplacesPayload(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.On(EventToolResult, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "rewritten"}, nil
	})
	env := &EventEnvelope{Event: EventToolResult, Payload: "original"}
	result := d.Emit(context.Background(), env)

	if result.Value != "rewritten" || env.Payload != "rewritten" {
		t.Fatalf("payload = %v, result = %v; want rewritten", env.Payload, result.Value)
	}
}

func TestDispatcherTransformChains(t *testing.T) {
	d := NewDispatcher()
	for _, suffix := range []string{"-a", "-b"} {
		suffix := suffix
		_, _ = d.On(EventContext, func(_ context.Context, env *EventEnvelope) (EventResult, error) {
			return EventResult{Value: env.Payload.(string) + suffix}, nil
		})
	}

	env := &EventEnvelope{Event: EventContext, Payload: "ctx"}
	result := d.Emit(context.Background(), env)
	if result.Value != "ctx-a-b" {
		t.Fatalf("Value = %q, want ctx-a-b", result.Value)
	}
	if env.Payload != "ctx-a-b" {
		t.Fatalf("envelope payload = %q, want chained value", env.Payload)
	}
}

func TestDispatcherTransformErrorFallsBack(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.On(EventContext, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "first"}, nil
	})
	_, _ = d.On(EventContext, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "broken"}, errors.New("transform failed")
	})
	var seen any
	_, _ = d.On(EventContext, func(_ context.Context, env *EventEnvelope) (EventResult, error) {
		seen = env.Payload
		return EventResult{}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventContext, Payload: "original"})
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1", len(result.Errors))
	}
	if seen != "first" {
		t.Fatalf("later handler saw %v, want previous value first", seen)
	}
	if result.Value != "first" {
		t.Fatalf("Value = %v, want first", result.Value)
	}
}

func TestDispatcherGateFirstDecisionWins(t *testing.T) {
	d := NewDispatcher()
	secondCalled := false
	_, _ = d.On(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Decision: DecisionBlock, Reason: "denied by policy"}, nil
	})
	_, _ = d.On(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		secondCalled = true
		return EventResult{Decision: DecisionBlock, Reason: "too late"}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventToolCall})
	if !result.Blocked() || result.Reason != "denied by policy" {
		t.Fatalf("result = %+v, want blocked by first handler", result)
	}
	if secondCalled {
		t.Fatal("gate should short-circuit after the first decision")
	}
}

func TestDispatcherGateHonorsPriority(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.OnWithOptions(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Decision: DecisionBlock, Reason: "low"}, nil
	}, HandlerOptions{Priority: 0})
	_, _ = d.OnWithOptions(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Decision: DecisionBlock, Reason: "high"}, nil
	}, HandlerOptions{Priority: 10})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventToolCall})
	if result.Reason != "high" {
		t.Fatalf("Reason = %q, want high-priority handler first", result.Reason)
	}
}

func TestDispatcherToolGateFailsClosedOnError(t *testing.T) {
	d := NewDispatcher()
	secondCalled := false
	_, _ = d.On(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{}, errors.New("boom")
	})
	_, _ = d.On(EventToolCall, func(context.Context, *EventEnvelope) (EventResult, error) {
		secondCalled = true
		return EventResult{}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventToolCall})
	if !result.Blocked() {
		t.Fatalf("result = %+v, want fail-closed block", result)
	}
	if !strings.Contains(result.Reason, "boom") {
		t.Fatalf("Reason = %q, want handler error surfaced", result.Reason)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1", len(result.Errors))
	}
	if secondCalled {
		t.Fatal("fail-closed gate should stop dispatch")
	}
}

func TestDispatcherNonFailClosedGateIsolatesErrors(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.On(EventProjectTrust, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{}, errors.New("trust store unavailable")
	})
	_, _ = d.On(EventProjectTrust, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Decision: DecisionBlock, Reason: "untrusted"}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventProjectTrust})
	if result.Decision != DecisionBlock || result.Reason != "untrusted" {
		t.Fatalf("result = %+v, want second handler's block", result)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1 isolated error", len(result.Errors))
	}
}

func TestDispatcherBoundaryContinueAndEnd(t *testing.T) {
	for _, decision := range []Decision{DecisionContinue, DecisionEnd} {
		decision := decision
		t.Run(string(decision), func(t *testing.T) {
			d := NewDispatcher()
			_, _ = d.On(EventTurnEnd, func(context.Context, *EventEnvelope) (EventResult, error) {
				return EventResult{Decision: decision, Reason: "boundary"}, nil
			})
			result := d.Emit(context.Background(), &EventEnvelope{Event: EventTurnEnd})
			if result.Decision != decision || result.Reason != "boundary" {
				t.Fatalf("result = %+v, want %q", result, decision)
			}
		})
	}
}

func TestDispatcherLastOverrideWins(t *testing.T) {
	d := NewDispatcher()
	_, _ = d.On(EventModelSelect, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "first"}, nil
	})
	_, _ = d.On(EventModelSelect, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "last"}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventModelSelect})
	if result.Value != "last" {
		t.Fatalf("Value = %v, want last", result.Value)
	}
}

func TestDispatcherLastOverrideHonorsPriority(t *testing.T) {
	d := NewDispatcher()
	// Low priority runs first, high priority runs last and wins.
	_, _ = d.OnWithOptions(EventModelSelect, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "high"}, nil
	}, HandlerOptions{Priority: 10})
	_, _ = d.OnWithOptions(EventModelSelect, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{Value: "low"}, nil
	}, HandlerOptions{Priority: 0})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventModelSelect})
	if result.Value != "high" {
		t.Fatalf("Value = %v, want high priority override", result.Value)
	}
}

func TestDispatcherIsolatesPanicsAndErrors(t *testing.T) {
	var reported []HandlerError
	d := NewDispatcher(WithErrorReporter(func(err HandlerError) { reported = append(reported, err) }))

	var order []string
	_, _ = d.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		order = append(order, "panic")
		panic("kaboom")
	})
	_, _ = d.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		order = append(order, "error")
		return EventResult{}, errors.New("handler error")
	})
	_, _ = d.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		order = append(order, "ok")
		return EventResult{}, nil
	})

	result := d.Emit(context.Background(), &EventEnvelope{Event: EventAgentStart})
	if !reflect.DeepEqual(order, []string{"panic", "error", "ok"}) {
		t.Fatalf("order = %v, want all handlers invoked", order)
	}
	if result.Handled != 3 || len(result.Errors) != 2 {
		t.Fatalf("Handled = %d, Errors = %d; want 3 and 2", result.Handled, len(result.Errors))
	}
	if len(reported) != 2 {
		t.Fatalf("reported = %d, want 2", len(reported))
	}
	if !strings.Contains(result.Errors[0].Error(), "panic") {
		t.Fatalf("first error = %v, want panic recovered", result.Errors[0])
	}
}

func TestDispatcherUnsubscribeIsIdempotent(t *testing.T) {
	d := NewDispatcher()
	called := 0
	unsubscribe, err := d.On(EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		called++
		return EventResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	unsubscribe()

	d.Emit(context.Background(), &EventEnvelope{Event: EventAgentStart})
	if called != 0 {
		t.Fatalf("called = %d, want 0 after unsubscribe", called)
	}
	if d.HandlerCount(EventAgentStart) != 0 {
		t.Fatalf("HandlerCount = %d, want 0", d.HandlerCount(EventAgentStart))
	}
}

func TestDispatcherSubscribeValidates(t *testing.T) {
	d := NewDispatcher()
	if _, err := d.On(Event("not_an_event"), func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{}, nil
	}); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("err = %v, want ErrUnknownEvent", err)
	}
	if _, err := d.On(EventAgentStart, nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("err = %v, want ErrNilHandler", err)
	}
}

func TestDispatcherUnsubscribeOwner(t *testing.T) {
	d := NewDispatcher()
	owner := "ext-a"
	if _, err := d.Subscribe(owner, SourceInfo{Kind: SourcePackage, ID: "pkg"}, EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{}, nil
	}, HandlerOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Subscribe("ext-b", SourceInfo{Kind: SourceUser}, EventAgentStart, func(context.Context, *EventEnvelope) (EventResult, error) {
		return EventResult{}, nil
	}, HandlerOptions{Priority: 3}); err != nil {
		t.Fatal(err)
	}

	if removed := d.UnsubscribeOwner(owner); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if d.HandlerCount(EventAgentStart) != 1 {
		t.Fatalf("HandlerCount = %d, want 1 remaining", d.HandlerCount(EventAgentStart))
	}

	subs := d.Subscriptions()
	if len(subs) != 1 || subs[0].Owner != "ext-b" || subs[0].Priority != 3 || subs[0].Event != EventAgentStart {
		t.Fatalf("subscriptions = %+v, want ext-b only", subs)
	}
}

func TestDispatcherEmitNilEnvelope(t *testing.T) {
	d := NewDispatcher()
	result := d.Emit(context.Background(), nil)
	if result.Handled != 0 || result.Decision != DecisionNone {
		t.Fatalf("result = %+v, want zero", result)
	}
}
