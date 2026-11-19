package telemetry

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPropagationRoundTripAcrossRecorders(t *testing.T) {
	caller := NewInMemoryTelemetry(WithScope("caller"))

	carrier := Carrier{}
	// Inject from a context without a span writes nothing.
	caller.Inject(context.Background(), carrier)
	if len(carrier) != 0 {
		t.Fatalf("inject without an active span wrote %d keys", len(carrier))
	}

	// Inject inside the callback context.
	if err := caller.StartSpan(context.Background(), SpanOptions{Name: "rpc.client"}, func(ctx context.Context, _ Span) error {
		caller.Inject(ctx, carrier)
		return nil
	}); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	if len(carrier) == 0 {
		t.Fatal("inject wrote an empty carrier")
	}

	wire, err := json.Marshal(carrier)
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	var decoded Carrier
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal carrier: %v", err)
	}

	receiver := NewInMemoryTelemetry(WithScope("receiver"))
	parent := receiver.Extract(decoded)
	// The caller span that injected is the first one; its id is deterministic.
	if parent.TraceID == "" || parent.SpanID != "0000000000000001" {
		t.Fatalf("extracted parent = %+v", parent)
	}

	var serverSpan SpanContext
	if err := receiver.StartSpan(context.Background(), SpanOptions{Name: "rpc.server", Parent: parent}, func(ctx context.Context, _ Span) error {
		serverSpan, _ = SpanFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	if serverSpan.TraceID != parent.TraceID {
		t.Fatalf("server trace = %q, want %q", serverSpan.TraceID, parent.TraceID)
	}
	spans := receiver.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected one receiver span, got %d", len(spans))
	}
	if spans[0].ParentID != parent.SpanID || !spans[0].RemoteParent {
		t.Fatalf("server parent = %q remote=%v, want %q remote", spans[0].ParentID, spans[0].RemoteParent, parent.SpanID)
	}
}

func TestPropagationInjectsDeepestContext(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("deep"))
	var carrier Carrier
	err := recorder.StartSpan(context.Background(), SpanOptions{Name: "root"}, func(ctx context.Context, _ Span) error {
		return recorder.StartSpan(ctx, SpanOptions{Name: "leaf"}, func(ctx context.Context, _ Span) error {
			leaf, _ := SpanFromContext(ctx)
			carrier = Carrier{}
			recorder.Inject(ctx, carrier)
			extracted := recorder.Extract(carrier)
			if extracted != leaf {
				t.Fatalf("round trip = %+v, want %+v", extracted, leaf)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("StartSpan returned error: %v", err)
	}
	if len(carrier) == 0 {
		t.Fatal("carrier is empty")
	}
}

func TestPropagationRejectsMalformedCarriers(t *testing.T) {
	recorder := NewInMemoryTelemetry(WithScope("malformed"))
	for name, carrier := range map[string]Carrier{
		"nil":        nil,
		"empty":      {},
		"unrelated":  {"traceparent": "00-abc-def-01"},
		"trace only": {carrierTraceID: "trace"},
		"span only":  {carrierSpanID: "span"},
		"blank":      {carrierTraceID: " ", carrierSpanID: " "},
	} {
		t.Run(name, func(t *testing.T) {
			if extracted := recorder.Extract(carrier); extracted.IsValid() {
				t.Fatalf("Extract(%v) = %+v, want invalid", carrier, extracted)
			}
		})
	}

	// Injection must not panic or allocate into a nil carrier.
	recorder.Inject(context.Background(), nil)
}
