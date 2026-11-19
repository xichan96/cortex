package telemetry

import "context"

// NoopTelemetry discards all telemetry. Use it as a default when telemetry is
// optional. The zero value and [Noop] are equivalent and safe concurrently.
type NoopTelemetry struct{}

var (
	_ Provider = NoopTelemetry{}
	_ Span     = noopSpan{}
)

// Noop is the shared no-op provider.
var Noop = NoopTelemetry{}

// StartSpan invokes fn with an inert span and the incoming context. The
// callback result is returned unchanged; no name, attribute, event, or status
// is retained.
func (NoopTelemetry) StartSpan(ctx context.Context, _ SpanOptions, fn SpanFunc) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	return fn(ctx, noopSpan{})
}

// Inject writes nothing.
func (NoopTelemetry) Inject(context.Context, Carrier) {}

// Extract reports no parent.
func (NoopTelemetry) Extract(Carrier) SpanContext { return SpanContext{} }

// Counter returns an instrument that discards measurements.
func (NoopTelemetry) Counter(string, InstrumentOptions) Counter { return noopInstrument{} }

// UpDownCounter returns an instrument that discards measurements.
func (NoopTelemetry) UpDownCounter(string, InstrumentOptions) UpDownCounter { return noopInstrument{} }

// Histogram returns an instrument that discards measurements.
func (NoopTelemetry) Histogram(string, HistogramOptions) Histogram { return noopInstrument{} }

type noopSpan struct{}

func (noopSpan) AddEvent(string, Attributes) {}
func (noopSpan) SetAttributes(Attributes)    {}
func (noopSpan) SetStatus(Status)            {}
func (noopSpan) AddLink(Link)                {}

// noopInstrument implements Counter, UpDownCounter, and Histogram.
type noopInstrument struct{}

func (noopInstrument) Add(context.Context, float64, Attributes)    {}
func (noopInstrument) Record(context.Context, float64, Attributes) {}
