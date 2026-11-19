package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(duration)
}

func TestSpanLifecycleHierarchyEventsStatusAndLinks(t *testing.T) {
	clock := newFakeClock()
	recorder := NewInMemoryTelemetry(WithScope("scope"), WithClock(clock.Now))

	var rootContext SpanContext
	var childContext SpanContext
	err := recorder.StartSpan(context.Background(), SpanOptions{
		Name:       "cortex.agent.run",
		Kind:       SpanKindServer,
		Attributes: Attributes{"cortex.agent.id": "run-1"},
	}, func(ctx context.Context, span Span) error {
		var ok bool
		rootContext, ok = SpanFromContext(ctx)
		if !ok {
			t.Fatal("root span context missing from callback context")
		}
		span.AddEvent("cortex.agent.step", Attributes{"cortex.agent.step_index": 0})
		span.SetAttributes(Attributes{"cortex.agent.step_count": 1, "cortex.agent.dropped": nil})
		span.SetAttributes(Attributes{"cortex.agent.step_count": 2, "cortex.agent.state": "running"})
		if got := recorder.Spans(); len(got) != 0 {
			t.Fatalf("unsettled spans were exposed: %d", len(got))
		}
		clock.Advance(5 * time.Millisecond)
		return recorder.StartSpan(ctx, SpanOptions{
			Name:       "cortex.tool.call",
			Kind:       SpanKindClient,
			Attributes: Attributes{"cortex.tool.name": "read"},
		}, func(ctx context.Context, child Span) error {
			childContext, ok = SpanFromContext(ctx)
			if !ok {
				t.Fatal("child span context missing from callback context")
			}
			child.AddEvent("cortex.tool.started", nil)
			child.AddLink(Link{
				Context:    rootContext,
				Attributes: Attributes{"cortex.tool.link_reason": "trigger"},
			})
			child.SetStatus(Status{Code: StatusError, Message: "tool failed"})
			clock.Advance(2 * time.Millisecond)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	if !rootContext.IsValid() || !childContext.IsValid() {
		t.Fatalf("invalid span contexts: root=%+v child=%+v", rootContext, childContext)
	}
	if rootContext.SpanID == childContext.SpanID {
		t.Fatal("child reused the root span id")
	}
	if rootContext.TraceID != childContext.TraceID {
		t.Fatalf("parent and child trace ids differ: %q vs %q", rootContext.TraceID, childContext.TraceID)
	}

	spans := recorder.Spans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	root, child := spans[0], spans[1]

	if root.ID != "0000000000000001" || root.TraceID != "scope-0000000000000001" {
		t.Fatalf("unexpected root identity: %+v", root)
	}
	if root.Name != "cortex.agent.run" || root.Kind != SpanKindServer {
		t.Fatalf("unexpected root name/kind: %q %q", root.Name, root.Kind)
	}
	if root.ParentID != "" || root.RemoteParent {
		t.Fatalf("root should have no parent: %+v", root)
	}
	if !root.Settled {
		t.Fatal("root should be settled")
	}
	if root.Status.Code != StatusOK {
		t.Fatalf("root status = %+v, want ok", root.Status)
	}
	if got := root.Attributes["cortex.agent.step_count"]; got != 2 {
		t.Fatalf("merged step_count = %v, want 2", got)
	}
	if _, present := root.Attributes["cortex.agent.dropped"]; present {
		t.Fatal("nil attribute value should have been ignored")
	}
	if root.Attributes["cortex.agent.state"] != "running" {
		t.Fatalf("missing merged attribute: %+v", root.Attributes)
	}
	if len(root.Events) != 1 || root.Events[0].Name != "cortex.agent.step" || root.Events[0].Offset != 0 {
		t.Fatalf("unexpected root events: %+v", root.Events)
	}
	if got := root.Duration(); got != 7*time.Millisecond {
		t.Fatalf("root duration = %v, want 7ms", got)
	}

	if child.ID != "0000000000000002" || child.TraceID != root.TraceID {
		t.Fatalf("unexpected child identity: %+v", child)
	}
	if child.ParentID != root.ID || child.RemoteParent {
		t.Fatalf("child parent = %q remote=%v, want %q local", child.ParentID, child.RemoteParent, root.ID)
	}
	if child.Status.Code != StatusError || child.Status.Message != "tool failed" {
		t.Fatalf("unexpected child status: %+v", child.Status)
	}
	if got := child.Duration(); got != 2*time.Millisecond {
		t.Fatalf("child duration = %v, want 2ms", got)
	}
	if len(child.Links) != 1 {
		t.Fatalf("expected one child link, got %d", len(child.Links))
	}
	if child.Links[0].Context != rootContext || child.Links[0].Attributes["cortex.tool.link_reason"] != "trigger" {
		t.Fatalf("unexpected link: %+v", child.Links[0])
	}

	if got := recorder.Trace(root.TraceID); len(got) != 2 {
		t.Fatalf("Trace returned %d spans, want 2", len(got))
	}
	if got := recorder.Trace("other-trace"); len(got) != 0 {
		t.Fatalf("Trace for unknown id returned %d spans", len(got))
	}

	// Snapshots are detached from the recorder and from one another.
	spans[0].Attributes["cortex.agent.step_count"] = 99
	spans[0].Events[0].Name = "mutated"
	fresh := recorder.Spans()
	if fresh[0].Attributes["cortex.agent.step_count"] != 2 || fresh[0].Events[0].Name != "cortex.agent.step" {
		t.Fatal("recorded spans were mutated through a snapshot")
	}
}

func TestNestedSpanUsesContextParent(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("nested"))
	var rootID, grandchildParentID string
	err := recorder.StartSpan(context.Background(), SpanOptions{Name: "root"}, func(ctx context.Context, _ Span) error {
		root, _ := SpanFromContext(ctx)
		rootID = root.SpanID
		return recorder.StartSpan(ctx, SpanOptions{Name: "middle"}, func(ctx context.Context, _ Span) error {
			middle, _ := SpanFromContext(ctx)
			return recorder.StartSpan(ctx, SpanOptions{Name: "leaf"}, func(ctx context.Context, _ Span) error {
				leaf, _ := SpanFromContext(ctx)
				grandchildParentID = middle.SpanID
				if leaf.TraceID != root.TraceID {
					t.Fatalf("leaf trace = %q, want %q", leaf.TraceID, root.TraceID)
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	spans := recorder.Spans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(spans))
	}
	if spans[1].ParentID != rootID || spans[2].ParentID != grandchildParentID {
		t.Fatalf("nested parentage is wrong: %+v", []RecordedSpan{spans[0], spans[1], spans[2]})
	}
}

func TestStartSpanStatusDerivation(t *testing.T) {
	sentinel := errors.New("boom")
	recorder := NewInMemoryTelemetry(WithScope("status"))

	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "ok"}, func(context.Context, Span) error {
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "error"}, func(context.Context, Span) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("StartSpan returned %v, want sentinel", err)
	}
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "explicit-error"}, func(_ context.Context, span Span) error {
		span.SetStatus(Status{Code: StatusError, Message: "expected failure"})
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "explicit-ok"}, func(_ context.Context, span Span) error {
		span.SetStatus(Status{Code: StatusOK})
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("StartSpan returned %v, want sentinel", err)
	}

	spans := recorder.Spans()
	if len(spans) != 4 {
		t.Fatalf("expected 4 spans, got %d", len(spans))
	}
	if spans[0].Status.Code != StatusOK {
		t.Fatalf("normal return status = %+v", spans[0].Status)
	}
	if spans[1].Status.Code != StatusError || spans[1].Status.Message != "boom" {
		t.Fatalf("error return status = %+v", spans[1].Status)
	}
	if spans[2].Status.Code != StatusError || spans[2].Status.Message != "expected failure" {
		t.Fatalf("explicit error status = %+v", spans[2].Status)
	}
	if spans[3].Status.Code != StatusOK {
		t.Fatalf("explicit ok status = %+v", spans[3].Status)
	}
}

func TestSpanMethodsAfterSettlementAreIgnored(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("settled"))
	var handle Span
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "span", Attributes: Attributes{"cortex.agent.id": "a"}}, func(_ context.Context, span Span) error {
		handle = span
		return nil
	}); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}

	handle.AddEvent("late", nil)
	handle.SetAttributes(Attributes{"cortex.agent.late": true})
	handle.SetStatus(Status{Code: StatusError, Message: "late"})
	handle.AddLink(Link{Context: SpanContext{TraceID: "t", SpanID: "s"}})

	spans := recorder.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}
	span := spans[0]
	if len(span.Events) != 0 || len(span.Links) != 0 {
		t.Fatalf("late events or links were recorded: %+v", span)
	}
	if _, present := span.Attributes["cortex.agent.late"]; present {
		t.Fatalf("late attributes were recorded: %+v", span.Attributes)
	}
	if span.Status.Code != StatusOK {
		t.Fatalf("late status was recorded: %+v", span.Status)
	}
}

func TestStartSpanPanicRecordsErrorAndRepanics(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("panic"))
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = recorder.StartSpan(context.Background(), SpanOptions{Name: "panicking"}, func(context.Context, Span) error {
			panic("boom")
		})
	}()
	if recovered != "boom" {
		t.Fatalf("panic value = %v, want boom", recovered)
	}
	spans := recorder.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}
	if spans[0].Status.Code != StatusError || !strings.Contains(spans[0].Status.Message, "panic: boom") {
		t.Fatalf("panic status = %+v", spans[0].Status)
	}
	if !spans[0].Settled {
		t.Fatal("panicking span was not settled")
	}
}

func TestStartSpanNilCallbackRecordsSettledSpan(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("nil"))
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "empty"}, nil); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	spans := recorder.Spans()
	if len(spans) != 1 || !spans[0].Settled || spans[0].Status.Code != StatusOK {
		t.Fatalf("unexpected spans: %+v", spans)
	}
}

func TestNoopTelemetryDiscardsEverything(t *testing.T) {
	sentinel := errors.New("sentinel")
	called := 0
	err := Noop.StartSpan(context.Background(), SpanOptions{Name: "ignored"}, func(ctx context.Context, span Span) error {
		called++
		span.AddEvent("event", nil)
		span.SetAttributes(Attributes{"cortex.agent.id": "a"})
		span.SetStatus(Status{Code: StatusError})
		span.AddLink(Link{Context: SpanContext{TraceID: "t", SpanID: "s"}})
		if _, ok := SpanFromContext(ctx); ok {
			t.Fatal("noop context should not expose a span")
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("StartSpan returned %v, want sentinel", err)
	}
	if called != 1 {
		t.Fatalf("callback called %d times, want 1", called)
	}
	if err := Noop.StartSpan(context.Background(), SpanOptions{Name: "nil"}, nil); err != nil {
		t.Fatalf("nil callback returned error: %v", err)
	}

	carrier := Carrier{}
	Noop.Inject(context.Background(), carrier)
	if len(carrier) != 0 {
		t.Fatalf("noop inject wrote %d keys", len(carrier))
	}
	if extracted := Noop.Extract(Carrier{"trace": "x"}); extracted.IsValid() {
		t.Fatalf("noop extract returned %+v", extracted)
	}

	Noop.Counter("c", InstrumentOptions{}).Add(context.Background(), 1, nil)
	Noop.UpDownCounter("u", InstrumentOptions{}).Add(context.Background(), -1, nil)
	Noop.Histogram("h", HistogramOptions{}).Record(context.Background(), 1, nil)
}

func TestContextSpanHelpers(t *testing.T) {
	if _, ok := SpanFromContext(context.Background()); ok {
		t.Fatal("background context unexpectedly carries a span")
	}
	if _, ok := SpanFromContext(nil); ok {
		t.Fatal("nil context unexpectedly carries a span")
	}
	if _, ok := SpanFromContext(ContextWithSpan(nil, SpanContext{})); ok {
		t.Fatal("invalid span context should not be reported")
	}
	want := SpanContext{TraceID: "trace", SpanID: "span"}
	got, ok := SpanFromContext(ContextWithSpan(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("round trip = %+v %v, want %+v true", got, ok, want)
	}
}
