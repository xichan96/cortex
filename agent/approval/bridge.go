package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RequestKind distinguishes the interactive requests a bridge can carry.
// Approval, question and plan all share the same request/response transport;
// only the payload and the UI rendering differ.
type RequestKind string

const (
	KindApproval RequestKind = "approval"
	KindQuestion RequestKind = "question"
	KindPlan     RequestKind = "plan"
)

// IsValid reports whether k is a declared kind.
func (k RequestKind) IsValid() bool {
	switch k {
	case KindApproval, KindQuestion, KindPlan:
		return true
	default:
		return false
	}
}

// BridgeRequest is one pending interaction. ID is optional on input: bridges
// generate one when empty. Timeout zero means wait until the caller's context
// is cancelled or the bridge is closed.
type BridgeRequest struct {
	ID        string          `json:"id"`
	Kind      RequestKind     `json:"kind"`
	SessionID string          `json:"session_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	Title     string          `json:"title,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timeout   time.Duration   `json:"timeout,omitempty"`
}

// BridgeResponse is the UI's answer to a BridgeRequest. Exactly one of
// Approved, Cancelled or Value is normally set; Error reports a UI-side
// failure. A response is delivered to the waiter matching its ID.
type BridgeResponse struct {
	ID        string          `json:"id"`
	Approved  bool            `json:"approved,omitempty"`
	Cancelled bool            `json:"cancelled,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Errors returned by bridges. Callers can distinguish timeouts, caller
// cancellation, duplicate/unknown request IDs and a closed bridge with
// errors.Is.
var (
	ErrBridgeTimeout        = errors.New("bridge request timed out")
	ErrBridgeCancelled      = errors.New("bridge request cancelled")
	ErrBridgeClosed         = errors.New("bridge closed")
	ErrUnknownRequest       = errors.New("unknown bridge request")
	ErrDuplicateRequest     = errors.New("duplicate bridge request id")
	ErrInvalidBridgeRequest = errors.New("invalid bridge request")
)

// Bridge is the transport-neutral request/response interface shared by
// approval, question and plan interactions. Implementations must honor ctx
// cancellation and return ErrBridgeTimeout when the request's own timeout
// expires.
type Bridge interface {
	Request(ctx context.Context, req BridgeRequest) (BridgeResponse, error)
}

// InteractiveBridge is a Bridge that also exposes its inbound request stream
// and accepts responses out of band. Gateways (for example the HTTP/SSE
// transport in agent/runtime/gateway) need both halves to relay requests to a
// UI and hand the UI's answers back to the waiting caller.
//
// Requests must be safe for a single consumer to read; a stream is not
// broadcast to multiple readers. Respond reports ErrUnknownRequest when no
// request with the response ID is pending.
type InteractiveBridge interface {
	Bridge
	Requests() <-chan BridgeRequest
	Respond(resp BridgeResponse) error
}

var (
	_ InteractiveBridge = (*MemoryBridge)(nil)
	_ InteractiveBridge = (*ChannelBridge)(nil)
)

func validateBridgeRequest(req BridgeRequest) error {
	if !req.Kind.IsValid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidBridgeRequest, req.Kind)
	}
	return nil
}

func timeoutError(id string) error {
	return fmt.Errorf("%w: %s", ErrBridgeTimeout, id)
}

func cancelledError(id string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w: %s: %w", ErrBridgeCancelled, id, cause)
	}
	return fmt.Errorf("%w: %s", ErrBridgeCancelled, id)
}

func closedError(id string) error {
	return fmt.Errorf("%w: %s", ErrBridgeClosed, id)
}

// bridgeCore holds the per-bridge pending-request table and ID generator that
// MemoryBridge and ChannelBridge share.
type bridgeCore struct {
	mu      sync.Mutex
	pending map[string]chan BridgeResponse
	closed  bool
	done    chan struct{}
	seq     atomic.Uint64
	prefix  string
}

func newBridgeCore() *bridgeCore {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failing is not recoverable on any supported platform;
		// the counter still keeps IDs unique within a process.
		raw = [4]byte{}
	}
	return &bridgeCore{
		pending: make(map[string]chan BridgeResponse),
		done:    make(chan struct{}),
		prefix:  "bridge-" + hex.EncodeToString(raw[:]),
	}
}

func (c *bridgeCore) nextID() string {
	return fmt.Sprintf("%s-%d", c.prefix, c.seq.Add(1))
}

func (c *bridgeCore) register(id string) (chan BridgeResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, closedError(id)
	}
	if _, ok := c.pending[id]; ok {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateRequest, id)
	}
	ch := make(chan BridgeResponse, 1)
	c.pending[id] = ch
	return ch, nil
}

func (c *bridgeCore) unregister(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// deliver routes resp to the waiting request. The pending entry is removed
// before delivery so a duplicate response is reported as unknown.
func (c *bridgeCore) deliver(resp BridgeResponse) error {
	if resp.ID == "" {
		return fmt.Errorf("%w: response has no id", ErrInvalidBridgeRequest)
	}
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	if ok {
		delete(c.pending, resp.ID)
	}
	closed := c.closed
	c.mu.Unlock()

	if !ok {
		if closed {
			return closedError(resp.ID)
		}
		return fmt.Errorf("%w: %s", ErrUnknownRequest, resp.ID)
	}
	ch <- resp
	close(ch)
	return nil
}

func (c *bridgeCore) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
}

// request validates req, assigns an ID, registers the waiter, hands the
// request to the transport and waits for the matching response.
func (c *bridgeCore) request(ctx context.Context, req BridgeRequest, out chan<- BridgeRequest) (BridgeResponse, error) {
	if err := validateBridgeRequest(req); err != nil {
		return BridgeResponse{ID: req.ID}, err
	}
	if req.ID == "" {
		req.ID = c.nextID()
	}
	ch, err := c.register(req.ID)
	if err != nil {
		return BridgeResponse{ID: req.ID}, err
	}

	var deadline time.Time
	if req.Timeout > 0 {
		deadline = time.Now().Add(req.Timeout)
	}

	if err := c.send(ctx, out, req, deadline); err != nil {
		c.unregister(req.ID)
		return interruptedResponse(req.ID, err), err
	}
	return c.await(ctx, req.ID, ch, deadline)
}

// send blocks until the request is queued, ctx/deadline expire, or the bridge
// closes.
func (c *bridgeCore) send(ctx context.Context, out chan<- BridgeRequest, req BridgeRequest, deadline time.Time) error {
	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return timeoutError(req.ID)
		}
		timer = time.NewTimer(remaining)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	select {
	case out <- req:
		return nil
	case <-ctx.Done():
		return cancelledError(req.ID, ctx.Err())
	case <-timeoutCh:
		return timeoutError(req.ID)
	case <-c.done:
		return closedError(req.ID)
	}
}

func (c *bridgeCore) await(ctx context.Context, id string, ch <-chan BridgeResponse, deadline time.Time) (BridgeResponse, error) {
	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if resp, ok := drain(ch); ok {
				return resp, nil
			}
			c.unregister(id)
			return BridgeResponse{ID: id}, timeoutError(id)
		}
		timer = time.NewTimer(remaining)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	// A response may already have been delivered while send/await were being
	// scheduled; prefer it over an expired timer.
	if resp, ok := drain(ch); ok {
		return resp, nil
	}

	select {
	case resp, ok := <-ch:
		if ok {
			return resp, nil
		}
		c.unregister(id)
		return BridgeResponse{ID: id}, closedError(id)
	case <-timeoutCh:
		if resp, ok := drain(ch); ok {
			return resp, nil
		}
		c.unregister(id)
		return BridgeResponse{ID: id}, timeoutError(id)
	case <-ctx.Done():
		if resp, ok := drain(ch); ok {
			return resp, nil
		}
		c.unregister(id)
		return BridgeResponse{ID: id, Cancelled: true}, cancelledError(id, ctx.Err())
	case <-c.done:
		if resp, ok := drain(ch); ok {
			return resp, nil
		}
		c.unregister(id)
		return BridgeResponse{ID: id, Cancelled: true}, closedError(id)
	}
}

func drain(ch <-chan BridgeResponse) (BridgeResponse, bool) {
	select {
	case resp, ok := <-ch:
		return resp, ok
	default:
		return BridgeResponse{}, false
	}
}

// interruptedResponse shapes the response returned when a request never
// reaches a decision. Timeouts are not cancellations; everything else is.
func interruptedResponse(id string, err error) BridgeResponse {
	return BridgeResponse{ID: id, Cancelled: !errors.Is(err, ErrBridgeTimeout)}
}

// MemoryBridge is an in-memory InteractiveBridge for tests and embedded
// hosts: the host reads requests from Requests() and answers with Respond.
// Its request queue is bounded; a caller blocks until the queue accepts the
// request, its context/timeout expires, or the bridge closes.
type MemoryBridge struct {
	core     *bridgeCore
	requests chan BridgeRequest

	closeOnce sync.Once
}

// NewMemoryBridge returns a MemoryBridge with no queued requests.
func NewMemoryBridge() *MemoryBridge {
	return &MemoryBridge{
		core:     newBridgeCore(),
		requests: make(chan BridgeRequest, 32),
	}
}

// Requests returns the inbound request stream. The channel is not closed by
// Close; consumers should also select on their request context.
func (b *MemoryBridge) Requests() <-chan BridgeRequest { return b.requests }

// Respond delivers resp to the waiting request. It returns ErrUnknownRequest
// for an unknown ID and ErrBridgeClosed after Close.
func (b *MemoryBridge) Respond(resp BridgeResponse) error { return b.core.deliver(resp) }

// Request implements Bridge.
func (b *MemoryBridge) Request(ctx context.Context, req BridgeRequest) (BridgeResponse, error) {
	return b.core.request(ctx, req, b.requests)
}

// Close fails pending and future requests. It is idempotent.
func (b *MemoryBridge) Close() error {
	b.closeOnce.Do(b.core.close)
	return nil
}

// ChannelBridge is an InteractiveBridge backed by host-facing channels: the
// host reads requests from Requests() and may either write responses to
// Responses() or call Respond. Responses are matched to waiters by ID.
type ChannelBridge struct {
	core      *bridgeCore
	requests  chan BridgeRequest
	responses chan BridgeResponse
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewChannelBridge returns a ChannelBridge and starts its response dispatcher.
// Call Close to stop the dispatcher.
func NewChannelBridge() *ChannelBridge {
	b := &ChannelBridge{
		core:      newBridgeCore(),
		requests:  make(chan BridgeRequest, 16),
		responses: make(chan BridgeResponse, 16),
	}
	b.wg.Add(1)
	go b.dispatch()
	return b
}

// Requests returns the inbound request stream. Only one consumer should read
// at a time; requests are distributed, not broadcast.
func (b *ChannelBridge) Requests() <-chan BridgeRequest { return b.requests }

// Responses is the optional host-side answer channel. Responses written here
// are routed by ID; responses for unknown IDs are dropped because a channel
// has no caller to report to. Use Respond when that error matters.
func (b *ChannelBridge) Responses() chan<- BridgeResponse { return b.responses }

// Respond delivers resp to the waiting request directly and reports unknown
// IDs, matching MemoryBridge.
func (b *ChannelBridge) Respond(resp BridgeResponse) error { return b.core.deliver(resp) }

// Request implements Bridge.
func (b *ChannelBridge) Request(ctx context.Context, req BridgeRequest) (BridgeResponse, error) {
	return b.core.request(ctx, req, b.requests)
}

func (b *ChannelBridge) dispatch() {
	defer b.wg.Done()
	for {
		select {
		case <-b.core.done:
			return
		case resp := <-b.responses:
			_ = b.core.deliver(resp)
		}
	}
}

// Close stops the dispatcher and fails pending and future requests. It is
// idempotent.
func (b *ChannelBridge) Close() error {
	b.closeOnce.Do(b.core.close)
	b.wg.Wait()
	return nil
}
