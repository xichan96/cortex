package hooks

// Tracer is the engine-held trace handle. nil = disabled (zero overhead: a
// pointer compare per recording point, no recorder object, no goroutine).
//
// The interface lives in agent/hooks (not dino/trace) so agent/engine can hold
// it without importing dino (design §3.2④, review B1 precedent — agent/engine
// never imports dino). dino/trace.Recorder implements it; dino/factory injects
// it via SetTracer.
type Tracer interface {
	// Record appends an event. Non-blocking: implementations must never block
	// the caller (the engine's hot path); dropping is an acceptable degradation.
	Record(ev TraceEvent)
	// Flush best-effort persists queued events (called at turn_end so a
	// completed turn is readable).
	Flush() error
	// Close drains, fsyncs, and closes. Idempotent.
	Close() error
}

// TraceEvent is the minimal event an engine recording point passes to a Tracer.
// The recorder fills envelope fields (seq/wall_time/trace_id/turn_id).
type TraceEvent struct {
	Type          string
	Iteration     *int
	ThreadID      string
	ParentTraceID string
	Payload       any // must be json.Marshal-able
}
