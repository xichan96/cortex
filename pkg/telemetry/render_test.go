package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRenderTraceGoldenOutput(t *testing.T) {
	clock := newFakeClock()
	recorder := NewInMemoryTelemetry(WithScope("t"), WithClock(clock.Now))

	var rootContext SpanContext
	err := recorder.StartSpan(context.Background(), SpanOptions{
		Name:       "cortex.agent.run",
		Kind:       SpanKindServer,
		Attributes: Attributes{"b": 2, "a": "x"},
	}, func(ctx context.Context, span Span) error {
		rootContext, _ = SpanFromContext(ctx)
		span.AddEvent("cortex.agent.step", Attributes{"cortex.agent.step_index": 0})
		clock.Advance(2 * time.Millisecond)
		return recorder.StartSpan(ctx, SpanOptions{
			Name:       "cortex.tool.call",
			Kind:       SpanKindClient,
			Attributes: Attributes{"cortex.tool.name": "read"},
		}, func(ctx context.Context, child Span) error {
			child.AddEvent("cortex.tool.started", nil)
			child.AddLink(Link{
				Context:    rootContext,
				Attributes: Attributes{"cortex.tool.link_reason": "trigger"},
			})
			clock.Advance(3 * time.Millisecond)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}

	want := strings.Join([]string{
		`cortex.agent.run kind=server a="x" b=2 (5ms)`,
		`  · cortex.agent.step cortex.agent.step_index=0 (+0ms)`,
		`  cortex.tool.call kind=client cortex.tool.name="read" (3ms)`,
		`    · cortex.tool.started (+0ms)`,
		`    → link trace=t-0000000000000001 span=0000000000000001 cortex.tool.link_reason="trigger"`,
	}, "\n")
	got := RenderTrace(recorder.Spans())
	if got != want {
		t.Fatalf("RenderTrace mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if again := RenderTrace(recorder.Spans()); again != got {
		t.Fatal("RenderTrace is not stable across identical inputs")
	}

	// Input order must not change the rendered forest.
	spans := recorder.Spans()
	reversed := []RecordedSpan{spans[1], spans[0]}
	if got := RenderTrace(reversed); got != want {
		t.Fatalf("RenderTrace depends on input order\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderTraceErrorsControlCharactersAndDuplicates(t *testing.T) {
	start := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	errorSpan := RecordedSpan{
		ID:        "2",
		TraceID:   "trace",
		ParentID:  "1",
		Name:      "bad\x1bname",
		Kind:      SpanKindInternal,
		Status:    Status{Code: StatusError, Message: "line\nbreak"},
		Settled:   true,
		StartTime: start,
		EndTime:   start.Add(1500 * time.Microsecond),
		Attributes: Attributes{
			"nested":  []string{"a", "b\nc"},
			"count":   3,
			"enabled": true,
		},
	}
	root := RecordedSpan{
		ID:        "1",
		TraceID:   "trace",
		Name:      "root",
		Settled:   true,
		StartTime: start,
		EndTime:   start.Add(2 * time.Millisecond),
		Links: []RecordedLink{{
			Context:    SpanContext{TraceID: "other\x00trace", SpanID: "span"},
			Attributes: Attributes{"reason": "trigger"},
		}},
	}

	got := RenderTrace([]RecordedSpan{root, errorSpan, root})
	want := strings.Join([]string{
		`root (2ms)`,
		`  → link trace=other\u0000trace span=span reason="trigger"`,
		`  bad\u001bname kind=internal count=3 enabled=true nested=["a","b\nc"] (1.5ms) error line\u000abreak`,
	}, "\n")
	if got != want {
		t.Fatalf("RenderTrace mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderTraceTreatsRemoteParentAsRoot(t *testing.T) {
	start := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	span := RecordedSpan{
		ID:           "2",
		TraceID:      "trace",
		ParentID:     "remote",
		RemoteParent: true,
		Name:         "orphan",
		Settled:      true,
		StartTime:    start,
		EndTime:      start,
	}
	if got, want := RenderTrace([]RecordedSpan{span}), "orphan (0ms)"; got != want {
		t.Fatalf("RenderTrace = %q, want %q", got, want)
	}
}

func TestRenderTraceErrorSpanStatus(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("status"))
	sentinel := errors.New("boom")
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "failing"}, func(context.Context, Span) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("StartSpan returned %v, want sentinel", err)
	}
	got := RenderTrace(recorder.Spans())
	if !strings.HasSuffix(got, " error boom") {
		t.Fatalf("RenderTrace = %q, want error suffix", got)
	}
}

func TestRenderTraceEmpty(t *testing.T) {
	if got := RenderTrace(nil); got != "" {
		t.Fatalf("RenderTrace(nil) = %q, want empty", got)
	}
	if got := RenderTrace([]RecordedSpan{}); got != "" {
		t.Fatalf("RenderTrace(empty) = %q, want empty", got)
	}
}
