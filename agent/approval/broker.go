package approval

import (
	"context"
	"errors"
	"time"
)

// Action is the outcome of an approval request.
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
)

// Sentinel errors a Broker may wrap when it cannot produce a decision. The
// pipeline maps them to the stable user-facing results "approval timed out"
// and "approval cancelled".
var (
	ErrApprovalDenied      = errors.New("approval denied")
	ErrApprovalTimeout     = errors.New("approval timed out")
	ErrApprovalCancelled   = errors.New("approval cancelled")
	ErrApprovalUnavailable = errors.New("approval broker unavailable")
)

// ApprovalRequest describes one authorization question. It is transport
// neutral: a UI/RPC bridge decides how to render it.
type ApprovalRequest struct {
	ID         string
	SessionID  string
	TurnID     string
	ToolCallID string
	ToolName   string
	Args       map[string]any
	Risk       RiskLevel
	Rule       string
	Scopes     []ApprovalScope
	// Timeout is the approval wait limit. Zero means wait forever (manual
	// approval mode), exactly like dino's ApprovalStore.
	Timeout time.Duration
}

// ApprovalResponse is the answer to an ApprovalRequest.
type ApprovalResponse struct {
	Decision Action
	Scope    ApprovalScope
	Remember bool
	Reason   string
}

// Allowed reports whether the response grants execution.
func (r ApprovalResponse) Allowed() bool { return r.Decision == ActionAllow }

// Broker answers approval requests. Implementations must honor ctx
// cancellation; a nil Broker is treated as deny by the pipeline (fail-closed).
type Broker interface {
	Request(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}

// FuncBroker adapts a function to the Broker interface.
type FuncBroker func(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)

// Request implements Broker.
func (f FuncBroker) Request(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	return f(ctx, req)
}

// AllowBroker approves everything. It is intended for tests and for hosts that
// have already gated execution elsewhere; never use it as a production default.
type AllowBroker struct{}

// Request implements Broker.
func (AllowBroker) Request(context.Context, ApprovalRequest) (ApprovalResponse, error) {
	return ApprovalResponse{Decision: ActionAllow, Scope: ScopeOnce}, nil
}

// DenyBroker denies everything. It is the explicit fail-closed implementation.
type DenyBroker struct {
	Reason string
}

// Request implements Broker.
func (b DenyBroker) Request(context.Context, ApprovalRequest) (ApprovalResponse, error) {
	reason := b.Reason
	if reason == "" {
		reason = "approval denied by policy"
	}
	return ApprovalResponse{Decision: ActionDeny, Reason: reason}, nil
}

// CallbackBroker adapts a host callback (the shape of the existing
// ApprovalSender) into a Broker.
type CallbackBroker struct {
	Fn func(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}

// Request implements Broker.
func (b CallbackBroker) Request(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	if b.Fn == nil {
		return ApprovalResponse{}, ErrApprovalUnavailable
	}
	return b.Fn(ctx, req)
}
