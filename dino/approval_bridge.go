package dino

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xichan96/cortex/agent/approval"
)

// bridgeBroker adapts a host-provided approval.Bridge to the transport-neutral
// approval.Broker interface used by the pipeline's authorize stage (design
// §5.4: TUI/Web only implement the bridge, never call ApprovalStore directly).
//
// The bridge request carries the tool call identity (ID is the pipeline call /
// tool-call ID), the routing context (session and turn), the tool name as the
// UI title, and the arguments as JSON. The bridge response is rendered with the
// approval bridge's semantics:
//
//	Approved            -> allow
//	Cancelled / !Approved -> deny (a decision, not an error)
//	Error               -> returned unchanged, so the pipeline stays fail-closed
//
// Transport failures (ctx cancellation, bridge timeout, closed bridge) are
// returned unchanged for the same reason; this broker never falls back to
// another channel, because a broken UI bridge must not silently prompt through
// a different transport or auto-approve.
type bridgeBroker struct {
	bridge approval.Bridge
}

// Request implements approval.Broker.
func (b *bridgeBroker) Request(ctx context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
	if b == nil || b.bridge == nil {
		return approval.ApprovalResponse{}, approval.ErrApprovalUnavailable
	}

	payload := json.RawMessage("{}")
	if len(req.Args) > 0 {
		data, err := json.Marshal(req.Args)
		if err != nil {
			// A non-marshalable argument map means the UI cannot render the
			// request; return the error rather than asking with a lossy payload.
			return approval.ApprovalResponse{}, fmt.Errorf("marshal approval arguments for %s: %w", req.ToolName, err)
		}
		payload = data
	}

	resp, err := b.bridge.Request(ctx, approval.BridgeRequest{
		Kind:      approval.KindApproval,
		ID:        req.ID,
		SessionID: req.SessionID,
		TurnID:    req.TurnID,
		Title:     req.ToolName,
		Payload:   payload,
		Timeout:   req.Timeout,
	})
	if err != nil {
		return approval.ApprovalResponse{}, err
	}
	if resp.Error != "" {
		return approval.ApprovalResponse{}, fmt.Errorf("approval bridge error for %s: %s", req.ToolName, resp.Error)
	}
	if resp.Cancelled {
		return approval.ApprovalResponse{
			Decision: approval.ActionDeny,
			Scope:    approval.ScopeOnce,
			Reason:   "approval cancelled",
		}, nil
	}
	if !resp.Approved {
		return approval.ApprovalResponse{
			Decision: approval.ActionDeny,
			Scope:    approval.ScopeOnce,
			Reason:   "approval denied",
		}, nil
	}
	// The generic bridge response has no scope/remember fields (those live in
	// Value for kind-specific payloads), so bridge-granted approvals are never
	// persisted by this broker. Remembered decisions already stored by the
	// pipeline are still honored before the broker is consulted.
	return approval.ApprovalResponse{
		Decision: approval.ActionAllow,
		Scope:    approval.ScopeOnce,
	}, nil
}
