// Package telemetry defines vendor-neutral telemetry contracts for cortex.
//
// The package models diagnostic work with spans, attributes, events, links,
// status, propagation, and metrics. It deliberately does not depend on
// OpenTelemetry, Sentry, or any other backend: applications implement
// [Provider] (or compose [NoopTelemetry] / [InMemoryTelemetry]) and may later
// attach an exporter in their own adapter package.
//
// # Contracts
//
// A [TelemetryContext] starts a span around a callback. The callback-scoped
// design guarantees that a span is closed exactly once: there is no public
// End method, and the span settles when the callback returns or panics.
// Normal completion records [StatusOK]; a returned error records
// [StatusError] unless the callback set an explicit status.
//
// A span is also the parent for nested work: the callback receives a
// [context.Context] carrying the span's [SpanContext], so a nested StartSpan
// with an empty [SpanOptions.Parent] becomes its child. Pass an explicit
// parent (for example one obtained from [Propagation.Extract]) to continue a
// trace across a transport or process boundary.
//
// # Security
//
// Attributes are control metadata only. Allowed values are strings, booleans,
// signed/unsigned integers, floats, and slices of those types. Instrumentation
// MUST NOT record prompts or completions, tool arguments or results, file
// contents, HTTP headers, credentials, provider payloads, or any other user
// content. [ValidateAttributes] enforces this with a deny-list key heuristic
// and value-shape checks; call it in instrumentation tests or at adapter
// boundaries, not on every span (recording stays allocation-free by default).
//
// Telemetry is diagnostics, not durable application state. Do not persist
// spans or carriers as business records. A carrier is optional correlation
// data and never carries authorization.
//
// # Naming
//
// Instrumentation is free to choose its vocabulary. The suggested namespaces
// are [NamespaceAgent] ("cortex.agent.*"), [NamespaceTool] ("cortex.tool.*"),
// and [NamespaceSession] ("cortex.session.*"). Adapters may translate these
// names for a backend without changing what cortex emits. Only low-cardinality
// labels belong in attributes; ids, names, counts, statuses, and durations are
// appropriate, request ids and user ids are not.
//
// # Implementations
//
// [NoopTelemetry] discards everything and is safe as a default.
// [InMemoryTelemetry] is a reference adapter with deterministic ids and
// queryable traces and measurements, intended for tests and local diagnostics.
// [RenderTrace] renders recorded spans as a stable text forest.
package telemetry
