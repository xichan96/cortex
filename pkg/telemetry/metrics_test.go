package telemetry

import (
	"context"
	"reflect"
	"testing"
)

func TestMetricsRecordingAndQueries(t *testing.T) {
	clock := newFakeClock()
	recorder := NewInMemoryTelemetry(WithScope("metrics"), WithClock(clock.Now))

	counter := recorder.Counter("cortex.tool.calls", InstrumentOptions{Description: "Tool calls", Unit: "{call}"})
	attributes := Attributes{"cortex.tool.name": "read", "cortex.tool.kinds": []string{"a", "b"}}
	counter.Add(context.Background(), 2, attributes)

	upDown := recorder.UpDownCounter("cortex.agent.active", InstrumentOptions{Description: "Active agents"})
	upDown.Add(context.Background(), -1, nil)

	boundaries := []float64{1, 10, 100}
	histogram := recorder.Histogram("cortex.tool.duration", HistogramOptions{
		InstrumentOptions: InstrumentOptions{Description: "Tool latency", Unit: "ms"},
		Boundaries:        boundaries,
	})

	var spanContext SpanContext
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "cortex.tool.call"}, func(ctx context.Context, _ Span) error {
		spanContext, _ = SpanFromContext(ctx)
		histogram.Record(ctx, 12.5, Attributes{"cortex.tool.name": "read"})
		return nil
	}); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}

	measurements := recorder.Measurements()
	if len(measurements) != 3 {
		t.Fatalf("expected 3 measurements, got %d", len(measurements))
	}

	calls := measurements[0]
	if calls.Kind != MetricCounter || calls.Name != "cortex.tool.calls" || calls.Value != 2 {
		t.Fatalf("unexpected counter measurement: %+v", calls)
	}
	if calls.Description != "Tool calls" || calls.Unit != "{call}" {
		t.Fatalf("counter options missing: %+v", calls)
	}
	if !reflect.DeepEqual(calls.Attributes["cortex.tool.kinds"], []string{"a", "b"}) {
		t.Fatalf("counter attributes = %+v", calls.Attributes)
	}
	if calls.Exemplar != nil {
		t.Fatalf("counter outside a span had an exemplar: %+v", calls.Exemplar)
	}

	active := measurements[1]
	if active.Kind != MetricUpDownCounter || active.Value != -1 || active.Name != "cortex.agent.active" {
		t.Fatalf("unexpected up-down measurement: %+v", active)
	}

	duration := measurements[2]
	if duration.Kind != MetricHistogram || duration.Value != 12.5 || duration.Unit != "ms" {
		t.Fatalf("unexpected histogram measurement: %+v", duration)
	}
	if !reflect.DeepEqual(duration.Boundaries, boundaries) {
		t.Fatalf("boundaries = %v, want %v", duration.Boundaries, boundaries)
	}
	if duration.Exemplar == nil || *duration.Exemplar != spanContext {
		t.Fatalf("histogram exemplar = %+v, want %+v", duration.Exemplar, spanContext)
	}

	// Inputs and query results are detached copies.
	attributes["cortex.tool.name"] = "mutated"
	attributes["cortex.tool.kinds"].([]string)[0] = "mutated"
	boundaries[0] = 999
	measurements[0].Attributes["cortex.tool.name"] = "mutated-again"

	fresh := recorder.Measurements()
	if fresh[0].Attributes["cortex.tool.name"] != "read" {
		t.Fatalf("recorded attributes share input storage: %+v", fresh[0].Attributes)
	}
	if got := fresh[0].Attributes["cortex.tool.kinds"].([]string)[0]; got != "a" {
		t.Fatalf("recorded slice shares input storage: %q", got)
	}
	if fresh[2].Boundaries[0] != 1 {
		t.Fatalf("recorded boundaries share input storage: %v", fresh[2].Boundaries)
	}
}

func TestMetricsWithoutSpanHaveNoExemplar(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("plain"))
	recorder.Histogram("plain", HistogramOptions{}).Record(context.Background(), 3, nil)
	measurements := recorder.Measurements()
	if len(measurements) != 1 || measurements[0].Exemplar != nil {
		t.Fatalf("unexpected measurements: %+v", measurements)
	}
	if measurements[0].Boundaries != nil {
		t.Fatalf("unexpected boundaries: %v", measurements[0].Boundaries)
	}
}

func TestResetDiscardsRecordedTelemetry(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("reset"))
	if err := recorder.StartSpan(context.Background(), SpanOptions{Name: "span"}, nil); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	recorder.Counter("counter", InstrumentOptions{}).Add(context.Background(), 1, nil)
	recorder.Reset()
	if spans := recorder.Spans(); len(spans) != 0 {
		t.Fatalf("spans after reset: %d", len(spans))
	}
	if measurements := recorder.Measurements(); len(measurements) != 0 {
		t.Fatalf("measurements after reset: %d", len(measurements))
	}
}

func TestInMemoryTelemetrySatisfiesProvider(t *testing.T) {
	var provider Provider = NewInMemoryTelemetry()
	_ = provider
	var noopProvider Provider = Noop
	_ = noopProvider
}
