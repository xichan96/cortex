package extensions

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Unsubscribe removes a subscription. It is safe to call more than once.
type Unsubscribe func()

// HandlerOptions tunes one event subscription.
type HandlerOptions struct {
	// Priority orders handlers. Gate handlers run highest priority first (the
	// first decisive verdict wins); last-override handlers run lowest priority
	// first (the last non-nil value wins). Other classes run in registration
	// order, as specified by design §7.3.
	Priority int
}

// EventBus is the extension-facing event surface.
type EventBus interface {
	// On subscribes with default options.
	On(event Event, handler Handler) (Unsubscribe, error)
	// OnWithOptions subscribes with explicit ordering options.
	OnWithOptions(event Event, handler Handler, opts HandlerOptions) (Unsubscribe, error)
	// Emit dispatches an event and returns the merged result. Handler errors are
	// isolated into EmitResult.Errors.
	Emit(ctx context.Context, env *EventEnvelope) EmitResult
}

// ErrorReporter receives isolated handler failures. It must not block for long;
// it is called on the emitting goroutine.
type ErrorReporter func(HandlerError)

// DispatcherOption configures a Dispatcher.
type DispatcherOption func(*Dispatcher)

// WithErrorReporter installs a hook that observes handler failures.
func WithErrorReporter(reporter ErrorReporter) DispatcherOption {
	return func(d *Dispatcher) {
		if reporter != nil {
			d.reporter = reporter
		}
	}
}

type handlerEntry struct {
	id       uint64
	owner    string
	source   SourceInfo
	priority int
	handler  Handler
}

// Dispatcher is a thread-safe, ordered event dispatcher implementing EventBus.
// The zero value is not usable; call NewDispatcher.
type Dispatcher struct {
	mu       sync.RWMutex
	seq      uint64
	handlers map[Event][]*handlerEntry
	reporter ErrorReporter
}

// NewDispatcher creates an empty dispatcher.
func NewDispatcher(opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{handlers: make(map[Event][]*handlerEntry)}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

var _ EventBus = (*Dispatcher)(nil)

// On subscribes to an event with default ordering.
func (d *Dispatcher) On(event Event, handler Handler) (Unsubscribe, error) {
	return d.Subscribe("", SourceInfo{Kind: SourceBuiltin}, event, handler, HandlerOptions{})
}

// OnWithOptions subscribes to an event with explicit ordering.
func (d *Dispatcher) OnWithOptions(event Event, handler Handler, opts HandlerOptions) (Unsubscribe, error) {
	return d.Subscribe("", SourceInfo{Kind: SourceBuiltin}, event, handler, opts)
}

// Subscribe registers a handler owned by owner. Unknown events and nil handlers
// are rejected so that typos surface at registration time.
func (d *Dispatcher) Subscribe(owner string, source SourceInfo, event Event, handler Handler, opts HandlerOptions) (Unsubscribe, error) {
	if !event.IsKnown() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEvent, event)
	}
	if handler == nil {
		return nil, ErrNilHandler
	}

	d.mu.Lock()
	d.seq++
	entry := &handlerEntry{
		id:       d.seq,
		owner:    owner,
		source:   source,
		priority: opts.Priority,
		handler:  handler,
	}
	d.handlers[event] = append(d.handlers[event], entry)
	d.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { d.remove(event, entry.id) })
	}, nil
}

func (d *Dispatcher) remove(event Event, id uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.handlers[event]
	for i, entry := range list {
		if entry.id != id {
			continue
		}
		kept := make([]*handlerEntry, 0, len(list)-1)
		kept = append(kept, list[:i]...)
		kept = append(kept, list[i+1:]...)
		if len(kept) == 0 {
			delete(d.handlers, event)
		} else {
			d.handlers[event] = kept
		}
		return
	}
}

// UnsubscribeOwner removes every handler registered by owner and returns how
// many were removed. The registry uses it to revoke a removed extension.
func (d *Dispatcher) UnsubscribeOwner(owner string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	removed := 0
	for event, list := range d.handlers {
		kept := list[:0]
		for _, entry := range list {
			if entry.owner == owner {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(d.handlers, event)
		} else {
			d.handlers[event] = kept
		}
	}
	return removed
}

// HandlerCount returns the number of handlers subscribed to event.
func (d *Dispatcher) HandlerCount(event Event) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers[event])
}

// EventRegistration describes one active subscription in a snapshot.
type EventRegistration struct {
	Event    Event
	Owner    string
	Source   SourceInfo
	Priority int
}

// Subscriptions returns a stable snapshot of all subscriptions, ordered by
// event name and then registration order.
func (d *Dispatcher) Subscriptions() []EventRegistration {
	d.mu.RLock()
	defer d.mu.RUnlock()

	events := make([]Event, 0, len(d.handlers))
	for event := range d.handlers {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })

	var out []EventRegistration
	for _, event := range events {
		list := append([]*handlerEntry(nil), d.handlers[event]...)
		sort.SliceStable(list, func(i, j int) bool { return list[i].id < list[j].id })
		for _, entry := range list {
			out = append(out, EventRegistration{
				Event:    event,
				Owner:    entry.owner,
				Source:   entry.source,
				Priority: entry.priority,
			})
		}
	}
	return out
}

// Emit dispatches env to every handler for its event and merges the results
// according to the event's classes:
//
//   - notification: handlers run for observation, results are ignored;
//   - mutation/transform: a non-nil Result.Value replaces the envelope payload
//     and is visible to later handlers (transform chaining);
//   - gate: the first decisive block wins and dispatch stops; for fail-closed
//     events a handler error also blocks;
//   - boundary: the first continue/end decision wins and dispatch stops;
//   - last-override: the last non-nil value wins.
//
// Handler errors never abort sibling handlers. They are collected in the
// result and passed to the configured ErrorReporter.
func (d *Dispatcher) Emit(ctx context.Context, env *EventEnvelope) EmitResult {
	var result EmitResult
	if env == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}

	event := env.Event
	spec := event.Spec()
	gate := hasClass(spec.Classes, ClassGate)
	mutation := hasClass(spec.Classes, ClassMutation)
	transform := hasClass(spec.Classes, ClassTransform)
	boundary := hasClass(spec.Classes, ClassBoundary)
	lastOverride := hasClass(spec.Classes, ClassLastOverride)
	handlers := d.orderedHandlers(event, gate, lastOverride)

	for _, entry := range handlers {
		if err := ctx.Err(); err != nil {
			// Cancellation is not a handler failure; stop dispatching quietly.
			break
		}

		out, err := callHandler(ctx, entry.handler, env)
		result.Handled++
		if err != nil {
			handlerErr := HandlerError{
				Event:  event,
				Owner:  entry.owner,
				Source: entry.source,
				Err:    err,
			}
			result.Errors = append(result.Errors, handlerErr)
			d.report(handlerErr)
			if spec.FailClosed && gate {
				result.Decision = DecisionBlock
				result.Reason = fmt.Sprintf("extension handler failed: %v", err)
				return result
			}
			continue
		}

		if out.Value != nil && (mutation || transform) {
			env.Payload = out.Value
			result.Value = out.Value
		}

		if gate && out.Decision == DecisionBlock {
			result.Decision = DecisionBlock
			result.Reason = out.Reason
			return result
		}

		if boundary && out.Decision != DecisionNone {
			result.Decision = out.Decision
			result.Reason = out.Reason
			return result
		}

		if lastOverride && out.Value != nil {
			result.Value = out.Value
		}
	}

	return result
}

// orderedHandlers snapshots and orders the subscriptions for one dispatch.
func (d *Dispatcher) orderedHandlers(event Event, gate, lastOverride bool) []*handlerEntry {
	d.mu.RLock()
	list := append([]*handlerEntry(nil), d.handlers[event]...)
	d.mu.RUnlock()

	sort.SliceStable(list, func(i, j int) bool {
		// Gate: high priority first so the strongest veto wins.
		if gate && list[i].priority != list[j].priority {
			return list[i].priority > list[j].priority
		}
		// Last-override: low priority first so the strongest value lands last.
		if lastOverride && list[i].priority != list[j].priority {
			return list[i].priority < list[j].priority
		}
		return list[i].id < list[j].id
	})
	return list
}

func hasClass(classes []EventClass, target EventClass) bool {
	for _, class := range classes {
		if class == target {
			return true
		}
	}
	return false
}

func (d *Dispatcher) report(err HandlerError) {
	if d.reporter != nil {
		d.reporter(err)
	}
}

// callHandler isolates panics as errors so one misbehaving extension cannot
// bring down the host.
func callHandler(ctx context.Context, handler Handler, env *EventEnvelope) (out EventResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("handler panic: %v", recovered)
			out = EventResult{}
		}
	}()
	return handler(ctx, env)
}

// ownedEventBus scopes subscriptions to one extension so the registry can
// revoke them when the extension is unloaded.
type ownedEventBus struct {
	dispatcher *Dispatcher
	owner      string
	source     SourceInfo
}

var _ EventBus = (*ownedEventBus)(nil)

func (b *ownedEventBus) On(event Event, handler Handler) (Unsubscribe, error) {
	return b.dispatcher.Subscribe(b.owner, b.source, event, handler, HandlerOptions{})
}

func (b *ownedEventBus) OnWithOptions(event Event, handler Handler, opts HandlerOptions) (Unsubscribe, error) {
	return b.dispatcher.Subscribe(b.owner, b.source, event, handler, opts)
}

func (b *ownedEventBus) Emit(ctx context.Context, env *EventEnvelope) EmitResult {
	return b.dispatcher.Emit(ctx, env)
}
