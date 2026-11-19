package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/types"
)

// AllowAllAuthorizer permits every call. It is for tests and for hosts that
// gate execution outside the pipeline; it is not a safe production default.
type AllowAllAuthorizer struct{}

// Authorize implements Authorizer.
func (AllowAllAuthorizer) Authorize(context.Context, *Call) (Decision, error) {
	return Continue(), nil
}

// DenyAllAuthorizer denies every call (fail-closed).
type DenyAllAuthorizer struct {
	Reason string
}

// Authorize implements Authorizer.
func (d DenyAllAuthorizer) Authorize(context.Context, *Call) (Decision, error) {
	reason := d.Reason
	if reason == "" {
		reason = "approval denied by policy"
	}
	return Block(reason), nil
}

// BrokerAuthorizer adapts an approval.Broker into the pipeline's authorize
// stage. It consults remembered decisions first, offers risk-appropriate scopes
// to the broker, enforces the approval timeout, and remembers approved scopes.
//
// A nil Broker yields approval.ErrApprovalUnavailable, which the pipeline turns
// into a fail-closed "blocked by policy" result.
type BrokerAuthorizer struct {
	Broker approval.Broker
	// Scopes are offered for low/medium-risk calls. Nil means approval.AllScopes.
	Scopes []approval.ApprovalScope
	// Timeout bounds the wait. Zero waits forever (manual approval mode).
	Timeout time.Duration
	// Risk overrides the risk lookup. Nil uses types.ToolRisk(c.Tool).
	Risk func(c *Call) types.RiskLevel
	// Rule names the matched rule/pattern, used for the remembered key.
	Rule func(c *Call) string
	// Store optionally supplies remembered allow/deny decisions and receives new
	// ones. Store errors never block execution.
	Store *approval.ScopeStore
}

// Authorize implements Authorizer.
func (a BrokerAuthorizer) Authorize(ctx context.Context, c *Call) (Decision, error) {
	risk := types.ToolRisk(c.Tool)
	if a.Risk != nil {
		risk = a.Risk(c)
	}
	rule := ""
	if a.Rule != nil {
		rule = a.Rule(c)
	}

	if a.Store != nil {
		if rec, ok := a.Store.Lookup(c.Name, rule); ok {
			if rec.Decision == approval.ActionDeny {
				return Block("remembered deny for " + c.Name), nil
			}
			return Continue(), nil
		}
	}

	if a.Broker == nil {
		return Decision{}, approval.ErrApprovalUnavailable
	}

	req := approval.ApprovalRequest{
		ID:         c.ID,
		SessionID:  c.SessionID,
		TurnID:     c.TurnID,
		ToolCallID: c.ID,
		ToolName:   c.Name,
		Args:       c.Args,
		Risk:       risk,
		Rule:       rule,
		Scopes:     approval.ScopesForRisk(risk, a.Scopes),
		Timeout:    a.Timeout,
	}

	resp, err := a.request(ctx, req)
	if err != nil {
		return Decision{}, err
	}
	if !resp.Allowed() {
		reason := resp.Reason
		if reason == "" {
			reason = "approval denied"
		}
		return Block(reason), nil
	}
	if a.Store != nil && resp.Remember {
		// A failure to persist must never block an approved call: the user
		// already said yes.
		_ = a.Store.Remember(req, resp)
	}
	return Continue(), nil
}

type brokerResult struct {
	resp approval.ApprovalResponse
	err  error
}

// request calls the broker, enforcing both the caller's context and the
// request's own Timeout. A broker that ignores ctx may leave a goroutine behind
// until it returns; the result channel is buffered so it cannot leak on send.
func (a BrokerAuthorizer) request(ctx context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()

	ch := make(chan brokerResult, 1)
	broker := a.Broker
	go func() {
		resp, err := broker.Request(reqCtx, req)
		ch <- brokerResult{resp: resp, err: err}
	}()

	var timer *time.Timer
	var timeoutC <-chan time.Time
	if a.Timeout > 0 {
		timer = time.NewTimer(a.Timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	select {
	case got := <-ch:
		if got.err != nil {
			switch {
			case errors.Is(got.err, approval.ErrApprovalTimeout),
				errors.Is(got.err, context.DeadlineExceeded):
				return approval.ApprovalResponse{}, approval.ErrApprovalTimeout
			case errors.Is(got.err, approval.ErrApprovalCancelled),
				errors.Is(got.err, context.Canceled):
				return approval.ApprovalResponse{}, approval.ErrApprovalCancelled
			default:
				return approval.ApprovalResponse{}, got.err
			}
		}
		return got.resp, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return approval.ApprovalResponse{}, approval.ErrApprovalTimeout
		}
		return approval.ApprovalResponse{}, approval.ErrApprovalCancelled
	case <-timeoutC:
		return approval.ApprovalResponse{}, approval.ErrApprovalTimeout
	}
}
