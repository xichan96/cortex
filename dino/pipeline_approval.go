package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/dino/permission"
)

// approvalStoreBroker adapts dino's ApprovalStore to the transport-neutral
// approval.Broker interface used by the pipeline's authorize stage (design
// §5.2: ChannelBroker / UI bridge). Session routing goes through
// req.SessionID, which the engine stamps per call.
type approvalStoreBroker struct {
	store *ApprovalStore
}

// Request implements approval.Broker. A missing store is treated as an
// unavailable broker, which the pipeline turns into a fail-closed block. Store
// errors (no sender, timeout, cancellation) are returned unchanged so the
// pipeline can classify them.
func (b *approvalStoreBroker) Request(ctx context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
	if b == nil || b.store == nil {
		return approval.ApprovalResponse{}, approval.ErrApprovalUnavailable
	}
	argsJSON := "{}"
	if len(req.Args) > 0 {
		data, err := json.Marshal(req.Args)
		if err != nil {
			return approval.ApprovalResponse{}, fmt.Errorf("marshal approval arguments for %s: %w", req.ToolName, err)
		}
		argsJSON = string(data)
	}
	approved, err := b.store.RequestApproval(ctx, req.SessionID, req.ToolName, argsJSON)
	if err != nil {
		return approval.ApprovalResponse{}, err
	}
	if approved {
		return approval.ApprovalResponse{Decision: approval.ActionAllow, Scope: approval.ScopeOnce}, nil
	}
	return approval.ApprovalResponse{Decision: approval.ActionDeny, Reason: "approval denied"}, nil
}

// permissionAuthorizer is the pipeline authorizer used when
// ToolPipeline.Authorizer == "approval". It evaluates the same ruleset as the
// session tool filter (dinoFactory.permissionRuleset), so the pipeline gate and
// the tool-exposure filter cannot drift apart:
//
//	allow -> continue
//	deny  -> block ("denied by permission: <tool>")
//	ask   -> ask the approval broker (BrokerAuthorizer, with timeout)
type permissionAuthorizer struct {
	evaluator *permission.Evaluator
	broker    approval.Broker
	timeout   time.Duration
}

// Authorize implements pipeline.Authorizer.
func (a *permissionAuthorizer) Authorize(ctx context.Context, c *pipeline.Call) (pipeline.Decision, error) {
	name := ""
	if c != nil {
		name = c.Name
	}
	if a == nil || a.evaluator == nil {
		return pipeline.Block("denied by permission: " + name), nil
	}
	// Evaluate by tool name, exactly like CreateSession's expose/deny filter.
	// Argument-level checks (workspace paths, .env reads) stay with the path
	// approval wrappers so both gates agree.
	switch action := a.evaluator.Evaluate(name, nil); action {
	case permission.ActionAllow:
		return pipeline.Continue(), nil
	case permission.ActionAsk:
		return pipeline.BrokerAuthorizer{Broker: a.broker, Timeout: a.timeout}.Authorize(ctx, c)
	default:
		return pipeline.Block("denied by permission: " + name), nil
	}
}
