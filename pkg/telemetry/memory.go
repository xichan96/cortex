package telemetry

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RecordedEvent is a settled event snapshot.
type RecordedEvent struct {
	Name       string
	Attributes Attributes
	// Offset is the event time relative to the span start.
	Offset time.Duration
}

// RecordedLink is a settled link snapshot.
type RecordedLink struct {
	Context    SpanContext
	Attributes Attributes
}

// RecordedSpan is a settled span snapshot, detached from the adapter.
type RecordedSpan struct {
	ID       string
	TraceID  string
	ParentID string
	// RemoteParent reports that ParentID refers to a span not recorded by
	// this adapter, typically one extracted from a carrier.
	RemoteParent bool
	Name         string
	Kind         SpanKind
	Attributes   Attributes
	Events       []RecordedEvent
	Links        []RecordedLink
	Status       Status
	StartTime    time.Time
	EndTime      time.Time
	Settled      bool
}

// Duration returns the recorded span duration, or zero before settlement.
func (s RecordedSpan) Duration() time.Duration {
	if !s.Settled || s.EndTime.Before(s.StartTime) {
		return 0
	}
	return s.EndTime.Sub(s.StartTime)
}

// MetricKind identifies a recorded instrument.
type MetricKind string

const (
	MetricCounter       MetricKind = "counter"
	MetricUpDownCounter MetricKind = "up_down_counter"
	MetricHistogram     MetricKind = "histogram"
)

// RecordedMeasurement is one recorded measurement, not an aggregate. Adapters
// and exporters own aggregation.
type RecordedMeasurement struct {
	Kind        MetricKind
	Name        string
	Description string
	Unit        string
	Boundaries  []float64
	Value       float64
	Attributes  Attributes
	// Exemplar is the span active when the measurement was recorded, if any.
	Exemplar *SpanContext
	Time     time.Time
}

// InMemoryOption configures [NewInMemoryTelemetry].
type InMemoryOption func(*inMemoryConfig)

type inMemoryConfig struct {
	scope string
	now   func() time.Time
}

// WithScope sets the recorder scope embedded in generated trace IDs. It is
// useful for stable expectations in tests and for telling independent
// recorders apart. The default is a process-unique "in-memory-N" scope.
func WithScope(scope string) InMemoryOption {
	return func(config *inMemoryConfig) {
		if scope != "" {
			config.scope = scope
		}
	}
}

// WithClock overrides the wall clock. It is primarily useful in tests.
func WithClock(now func() time.Time) InMemoryOption {
	return func(config *inMemoryConfig) {
		if now != nil {
			config.now = now
		}
	}
}

// InMemoryTelemetry is a process-local reference adapter implementing
// [Provider]. It records detached snapshots queryable with [InMemoryTelemetry.Spans],
// [InMemoryTelemetry.Trace], and [InMemoryTelemetry.Measurements].
//
// Storage is unbounded and process-local; create a fresh instance to isolate
// tests or recording scopes. The zero value is usable, but [NewInMemoryTelemetry]
// is preferred. All methods are safe for concurrent use.
type InMemoryTelemetry struct {
	mu           sync.Mutex
	scope        string
	now          func() time.Time
	nextSpanID   uint64
	spans        []*mutableSpan
	byID         map[string]*mutableSpan
	measurements []RecordedMeasurement
}

var (
	_ Provider = (*InMemoryTelemetry)(nil)
	_ Span     = (*inMemorySpan)(nil)
)

var defaultScopeCounter atomic.Uint64

// NewInMemoryTelemetry returns a recorder. Options are applied in order.
func NewInMemoryTelemetry(options ...InMemoryOption) *InMemoryTelemetry {
	config := inMemoryConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return &InMemoryTelemetry{
		scope: config.scope,
		now:   config.now,
		byID:  make(map[string]*mutableSpan),
	}
}

type mutableSpan struct {
	id             string
	traceID        string
	parentID       string
	remoteParent   bool
	name           string
	kind           SpanKind
	start          time.Time
	end            time.Time
	attributes     Attributes
	events         []RecordedEvent
	links          []RecordedLink
	status         Status
	explicitStatus bool
	settled        bool
}

type inMemorySpan struct {
	telemetry *InMemoryTelemetry
	span      *mutableSpan
}

// StartSpan implements [TelemetryContext]. The span settles when fn returns or
// panics; a panic is re-raised after the error status is recorded.
func (t *InMemoryTelemetry) StartSpan(ctx context.Context, options SpanOptions, fn SpanFunc) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	parent := options.Parent
	if !parent.IsValid() {
		if fromContext, ok := SpanFromContext(ctx); ok {
			parent = fromContext
		}
	}
	kind := options.Kind
	if kind == "" {
		kind = SpanKindInternal
	}

	t.mu.Lock()
	if t.byID == nil {
		t.byID = make(map[string]*mutableSpan)
	}
	scope := t.scopeLocked()
	start := options.StartTime
	if start.IsZero() {
		start = t.nowLocked()
	}
	id := t.newSpanIDLocked()
	traceID := parent.TraceID
	if traceID == "" {
		traceID = scope + "-" + id
	}
	remoteParent := false
	if parent.IsValid() {
		if _, local := t.byID[parent.SpanID]; !local {
			remoteParent = true
		}
	}
	span := &mutableSpan{
		id:           id,
		traceID:      traceID,
		parentID:     parent.SpanID,
		remoteParent: remoteParent,
		name:         options.Name,
		kind:         kind,
		start:        start,
		attributes:   copyAttributes(options.Attributes),
		links:        copyLinks(options.Links),
		status:       Status{Code: StatusUnset},
	}
	t.spans = append(t.spans, span)
	t.byID[id] = span
	t.mu.Unlock()

	spanContext := SpanContext{TraceID: traceID, SpanID: id}
	childContext := ContextWithSpan(ctx, spanContext)
	defer func() {
		if recovered := recover(); recovered != nil {
			t.finish(span, fmt.Errorf("panic: %v", recovered))
			panic(recovered)
		}
	}()
	if fn != nil {
		err = fn(childContext, &inMemorySpan{telemetry: t, span: span})
	}
	t.finish(span, err)
	return err
}

func (t *InMemoryTelemetry) finish(span *mutableSpan, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if span.settled {
		return
	}
	span.settled = true
	span.end = t.nowLocked()
	if span.end.Before(span.start) {
		span.end = span.start
	}
	if !span.explicitStatus {
		if err != nil {
			span.status = Status{Code: StatusError, Message: err.Error()}
		} else {
			span.status = Status{Code: StatusOK}
		}
	}
}

func (t *InMemoryTelemetry) scopeLocked() string {
	if t.scope == "" {
		t.scope = fmt.Sprintf("in-memory-%d", defaultScopeCounter.Add(1))
	}
	return t.scope
}

func (t *InMemoryTelemetry) nowLocked() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *InMemoryTelemetry) newSpanIDLocked() string {
	t.nextSpanID++
	return fmt.Sprintf("%016x", t.nextSpanID)
}

func (s *inMemorySpan) AddEvent(name string, attributes Attributes) {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	if s.span.settled {
		return
	}
	offset := s.telemetry.nowLocked().Sub(s.span.start)
	if offset < 0 {
		offset = 0
	}
	s.span.events = append(s.span.events, RecordedEvent{
		Name:       name,
		Attributes: copyAttributes(attributes),
		Offset:     offset,
	})
}

func (s *inMemorySpan) SetAttributes(attributes Attributes) {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	if s.span.settled {
		return
	}
	for key, value := range attributes {
		if value == nil {
			continue
		}
		if s.span.attributes == nil {
			s.span.attributes = make(Attributes, len(attributes))
		}
		s.span.attributes[key] = copyAttributeValue(value)
	}
}

func (s *inMemorySpan) SetStatus(status Status) {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	if s.span.settled {
		return
	}
	if status.Code == "" {
		status.Code = StatusUnset
	}
	s.span.status = status
	s.span.explicitStatus = status.Code != StatusUnset
}

func (s *inMemorySpan) AddLink(link Link) {
	if !link.Context.IsValid() {
		return
	}
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	if s.span.settled {
		return
	}
	s.span.links = append(s.span.links, RecordedLink{
		Context:    link.Context,
		Attributes: copyAttributes(link.Attributes),
	})
}

// Spans returns settled spans in start order. Symbols are detached copies.
func (t *InMemoryTelemetry) Spans() []RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	spans := make([]RecordedSpan, 0, len(t.spans))
	for _, span := range t.spans {
		if span.settled {
			spans = append(spans, span.snapshot())
		}
	}
	return spans
}

// Trace returns settled spans with the given trace ID, in start order.
func (t *InMemoryTelemetry) Trace(traceID string) []RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	spans := make([]RecordedSpan, 0, len(t.spans))
	for _, span := range t.spans {
		if span.settled && span.traceID == traceID {
			spans = append(spans, span.snapshot())
		}
	}
	return spans
}

// Measurements returns recorded measurements in recording order.
func (t *InMemoryTelemetry) Measurements() []RecordedMeasurement {
	t.mu.Lock()
	defer t.mu.Unlock()
	measurements := make([]RecordedMeasurement, len(t.measurements))
	for index, measurement := range t.measurements {
		measurements[index] = copyMeasurement(measurement)
	}
	return measurements
}

// Reset discards recorded spans and measurements. Span ids keep increasing.
func (t *InMemoryTelemetry) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.spans = nil
	t.byID = make(map[string]*mutableSpan)
	t.measurements = nil
}

// Inject implements [Propagation] using adapter-owned keys.
func (t *InMemoryTelemetry) Inject(ctx context.Context, carrier Carrier) {
	if carrier == nil {
		return
	}
	spanContext, ok := SpanFromContext(ctx)
	if !ok {
		return
	}
	carrier[carrierTraceID] = spanContext.TraceID
	carrier[carrierSpanID] = spanContext.SpanID
}

// Extract implements [Propagation]. Absent, empty, or incomplete carriers
// yield an invalid context; identifiers are opaque and not format-checked.
func (t *InMemoryTelemetry) Extract(carrier Carrier) SpanContext {
	if carrier == nil {
		return SpanContext{}
	}
	traceID := carrier[carrierTraceID]
	spanID := carrier[carrierSpanID]
	// Identifiers are opaque, but a blank carrier cannot name a span.
	if strings.TrimSpace(traceID) == "" || strings.TrimSpace(spanID) == "" {
		return SpanContext{}
	}
	return SpanContext{TraceID: traceID, SpanID: spanID}
}

const (
	carrierTraceID = "cortex.trace-id"
	carrierSpanID  = "cortex.span-id"
)

// Counter implements [Metrics].
func (t *InMemoryTelemetry) Counter(name string, options InstrumentOptions) Counter {
	return &inMemoryCounter{telemetry: t, kind: MetricCounter, name: name, description: options.Description, unit: options.Unit}
}

// UpDownCounter implements [Metrics].
func (t *InMemoryTelemetry) UpDownCounter(name string, options InstrumentOptions) UpDownCounter {
	return &inMemoryCounter{telemetry: t, kind: MetricUpDownCounter, name: name, description: options.Description, unit: options.Unit}
}

// Histogram implements [Metrics].
func (t *InMemoryTelemetry) Histogram(name string, options HistogramOptions) Histogram {
	return &inMemoryHistogram{
		telemetry:   t,
		name:        name,
		description: options.Description,
		unit:        options.Unit,
		boundaries:  append([]float64(nil), options.Boundaries...),
	}
}

type inMemoryCounter struct {
	telemetry   *InMemoryTelemetry
	kind        MetricKind
	name        string
	description string
	unit        string
}

func (c *inMemoryCounter) Add(ctx context.Context, value float64, attributes Attributes) {
	c.telemetry.record(c.kind, c.name, c.description, c.unit, nil, value, attributes, ctx)
}

type inMemoryHistogram struct {
	telemetry   *InMemoryTelemetry
	name        string
	description string
	unit        string
	boundaries  []float64
}

func (h *inMemoryHistogram) Record(ctx context.Context, value float64, attributes Attributes) {
	h.telemetry.record(MetricHistogram, h.name, h.description, h.unit, h.boundaries, value, attributes, ctx)
}

func (t *InMemoryTelemetry) record(kind MetricKind, name, description, unit string, boundaries []float64, value float64, attributes Attributes, ctx context.Context) {
	exemplar, hasExemplar := SpanFromContext(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	measurement := RecordedMeasurement{
		Kind:        kind,
		Name:        name,
		Description: description,
		Unit:        unit,
		Value:       value,
		Attributes:  copyAttributes(attributes),
		Time:        t.nowLocked(),
	}
	if boundaries != nil {
		measurement.Boundaries = append([]float64(nil), boundaries...)
	}
	if hasExemplar && exemplar.IsValid() {
		copied := exemplar
		measurement.Exemplar = &copied
	}
	t.measurements = append(t.measurements, measurement)
}

func (s *mutableSpan) snapshot() RecordedSpan {
	events := make([]RecordedEvent, len(s.events))
	for index, event := range s.events {
		events[index] = RecordedEvent{
			Name:       event.Name,
			Attributes: copyAttributes(event.Attributes),
			Offset:     event.Offset,
		}
	}
	links := make([]RecordedLink, len(s.links))
	for index, link := range s.links {
		links[index] = RecordedLink{
			Context:    link.Context,
			Attributes: copyAttributes(link.Attributes),
		}
	}
	return RecordedSpan{
		ID:           s.id,
		TraceID:      s.traceID,
		ParentID:     s.parentID,
		RemoteParent: s.remoteParent,
		Name:         s.name,
		Kind:         s.kind,
		Attributes:   copyAttributes(s.attributes),
		Events:       events,
		Links:        links,
		Status:       s.status,
		StartTime:    s.start,
		EndTime:      s.end,
		Settled:      s.settled,
	}
}

func copyMeasurement(measurement RecordedMeasurement) RecordedMeasurement {
	copied := RecordedMeasurement{
		Kind:        measurement.Kind,
		Name:        measurement.Name,
		Description: measurement.Description,
		Unit:        measurement.Unit,
		Value:       measurement.Value,
		Attributes:  copyAttributes(measurement.Attributes),
		Time:        measurement.Time,
	}
	if measurement.Boundaries != nil {
		copied.Boundaries = append([]float64(nil), measurement.Boundaries...)
	}
	if measurement.Exemplar != nil {
		exemplar := *measurement.Exemplar
		copied.Exemplar = &exemplar
	}
	return copied
}

func copyLinks(links []Link) []RecordedLink {
	copied := make([]RecordedLink, 0, len(links))
	for _, link := range links {
		if !link.Context.IsValid() {
			continue
		}
		copied = append(copied, RecordedLink{
			Context:    link.Context,
			Attributes: copyAttributes(link.Attributes),
		})
	}
	return copied
}

func copyAttributes(attributes Attributes) Attributes {
	if len(attributes) == 0 {
		return nil
	}
	copied := make(Attributes, len(attributes))
	for key, value := range attributes {
		if value == nil {
			continue
		}
		copied[key] = copyAttributeValue(value)
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func copyAttributeValue(value any) any {
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Slice {
		copied := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
		reflect.Copy(copied, reflected)
		return copied.Interface()
	}
	return value
}
