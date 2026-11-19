package telemetry

import (
	"context"
	"time"
)

// Attributes is an open bag of diagnostic facts attached to a span, event, or
// measurement. Keys should be dotted names such as "cortex.tool.name".
//
// Allowed values are string, bool, signed and unsigned integers, floats, and
// slices of those types. Maps, structs, pointers, byte slices, and nil values
// are not valid. See the package documentation for the security policy and
// [ValidateAttributes] for a checker.
type Attributes map[string]any

// SpanKind describes the role of a span in a trace. An empty kind defaults to
// [SpanKindInternal].
type SpanKind string

const (
	SpanKindInternal SpanKind = "internal"
	SpanKindServer   SpanKind = "server"
	SpanKindClient   SpanKind = "client"
	SpanKindProducer SpanKind = "producer"
	SpanKindConsumer SpanKind = "consumer"
)

// StatusCode is the outcome of a span.
type StatusCode string

const (
	// StatusUnset is the zero status. Adapters derive the final status from
	// the callback result when no explicit status was set.
	StatusUnset StatusCode = "unset"
	StatusOK    StatusCode = "ok"
	StatusError StatusCode = "error"
)

// Status records a span outcome. Message is free-form diagnostic text and must
// not contain user payloads.
type Status struct {
	Code    StatusCode
	Message string
}

// IsError reports whether the status code is [StatusError].
func (s Status) IsError() bool { return s.Code == StatusError }

// SpanContext identifies a span independently of a live span handle. TraceID
// and SpanID are opaque strings owned by the adapter; they are suitable for
// carriers but carry no authorization meaning.
type SpanContext struct {
	TraceID string
	SpanID  string
}

// IsValid reports whether both identifiers are present.
func (s SpanContext) IsValid() bool { return s.TraceID != "" && s.SpanID != "" }

// Link relates work without making it a child. Context may be local or
// extracted from a carrier.
type Link struct {
	Context    SpanContext
	Attributes Attributes
}

// SpanOptions configures [TelemetryContext.StartSpan].
type SpanOptions struct {
	// Name is the span name, for example "cortex.tool.call".
	Name string
	// Kind defaults to SpanKindInternal.
	Kind SpanKind
	// Attributes are recorded at start.
	Attributes Attributes
	// Parent is an explicit parent. When it is invalid, StartSpan uses the
	// span stored in the incoming context, falling back to a new root.
	Parent SpanContext
	// Links relate this span to other contexts without parenting.
	Links []Link
	// StartTime overrides the wall-clock start time. Zero means adapter clock.
	StartTime time.Time
}

// Span is the recording handle passed to a [SpanFunc]. All methods are safe to
// call after settlement; adapters ignore them. A span settles when the
// callback returns or panics, so there is no public End method.
type Span interface {
	// AddEvent records a point-in-time occurrence with optional attributes.
	AddEvent(name string, attributes Attributes)
	// SetAttributes merges attributes; later calls replace earlier values and
	// nil values are ignored.
	SetAttributes(attributes Attributes)
	// SetStatus sets the final status explicitly. Last write wins.
	SetStatus(status Status)
	// AddLink appends a link to another span context.
	AddLink(link Link)
}

// SpanFunc is the callback scoped by [TelemetryContext.StartSpan]. The context
// carries the new span's [SpanContext], so nested StartSpan calls become
// children automatically.
type SpanFunc func(ctx context.Context, span Span) error

// TelemetryContext starts callback-scoped spans. Adapters implement this
// interface; call sites accept a TelemetryContext and may use the zero value
// [NoopTelemetry] when telemetry is optional.
type TelemetryContext interface {
	// StartSpan creates a span, invokes fn exactly once, and settles the span
	// when fn returns or panics. If fn is nil the span is created and settled
	// without invoking anything. The returned error is fn's error unchanged.
	StartSpan(ctx context.Context, options SpanOptions, fn SpanFunc) error
}

// Carrier is opaque, JSON-serializable propagation data. Adapters own its
// format; W3C traceparent and tracestate fit without being required. Transport
// code sends only the carrier, never a span or adapter.
type Carrier map[string]string

// Propagation injects and extracts correlation data across process or
// transport boundaries. Both methods are synchronous. Injection writes nothing
// for roots and inactive contexts. Extraction returns an invalid SpanContext
// for absent, empty, or unknown carriers, and callers then start a new root.
type Propagation interface {
	Inject(ctx context.Context, carrier Carrier)
	Extract(carrier Carrier) SpanContext
}

// InstrumentOptions describes a metric instrument.
type InstrumentOptions struct {
	Description string
	Unit        string
}

// HistogramOptions describes a histogram. Boundaries are strictly increasing
// bucket hints; adapters own aggregation.
type HistogramOptions struct {
	InstrumentOptions
	Boundaries []float64
}

// Counter records monotonic, non-negative increments.
type Counter interface {
	Add(ctx context.Context, value float64, attributes Attributes)
}

// UpDownCounter records increments and decrements.
type UpDownCounter interface {
	Add(ctx context.Context, value float64, attributes Attributes)
}

// Histogram records measurements. When ctx carries an active span, adapters
// may record it as an exemplar.
type Histogram interface {
	Record(ctx context.Context, value float64, attributes Attributes)
}

// Metrics exposes the metric capabilities of a provider.
type Metrics interface {
	Counter(name string, options InstrumentOptions) Counter
	UpDownCounter(name string, options InstrumentOptions) UpDownCounter
	Histogram(name string, options HistogramOptions) Histogram
}

// Provider combines the tracing, propagation, and metrics capabilities that a
// configured adapter offers. Applications hold one Provider and pass narrower
// interfaces to code that does not need all three.
type Provider interface {
	TelemetryContext
	Propagation
	Metrics
}

// Namespaces suggested for cortex instrumentation. They are conventions, not
// validation rules; adapters may translate them for their backend.
const (
	NamespaceAgent   = "cortex.agent"
	NamespaceTool    = "cortex.tool"
	NamespaceSession = "cortex.session"
)

type spanContextKey struct{}

// ContextWithSpan returns a context carrying sc. It is used by StartSpan and
// by adapters that need to expose an extracted parent to nested work.
func ContextWithSpan(ctx context.Context, sc SpanContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, spanContextKey{}, sc)
}

// SpanFromContext returns the active span context, if any.
func SpanFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	if !ok || !sc.IsValid() {
		return SpanContext{}, false
	}
	return sc, true
}
