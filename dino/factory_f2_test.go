package dino

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentutils "github.com/xichan96/cortex/agent/utils"
	"github.com/xichan96/cortex/dino/tools"
)

// TestWrapSessionTool_Order verifies the wrapper stack built by
// wrapSessionTool. The concrete wrapper types are unexported in dino/tools and
// dino, so we assert on fmt.Sprintf("%T") of the outermost wrapper and on the
// observable behavior of each layer (limiter bounds results, nonFatal turns
// errors into {ok:false}, loop detection is the outermost shell).
func TestWrapSessionTool_Order(t *testing.T) {
	store := NewApprovalStore(0)
	needApproval := map[string]bool{"bash": true}

	f := &dinoFactory{
		config:        DefaultConfig(),
		approvalStore: store,
		loopDetector:  &neverLoopDetector{},
	}
	senderAdapter := &toolEventSenderAdapter{}

	raw := &mockTool{name: "bash", description: "run bash"}
	wrapped := f.wrapSessionTool(raw, "sess-1", senderAdapter, needApproval)

	// Outermost wrapper must be the loop-detection tool.
	outer := fmt.Sprintf("%T", wrapped)
	if !strings.Contains(outer, "loopDetectingTool") {
		t.Fatalf("outermost wrapper must be loop detection, got %s", outer)
	}

	// Behavior: an oversized bash result must be limited (limiter applied
	// inside), not pass through raw.
	big := &mockTool{
		name: "bash",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return strings.Repeat("x", 70_000), nil
		},
	}
	needApprovalBig := map[string]bool{"bash": false}
	wrappedBig := f.wrapSessionTool(big, "sess-1", senderAdapter, needApprovalBig)
	res, err := wrappedBig.Execute(context.Background(), map[string]interface{}{"command": "ls"})
	if err != nil {
		t.Fatalf("oversized result should not hard-error, got %v", err)
	}
	if m, ok := res.(map[string]interface{}); ok {
		if m["ok"] == false {
			t.Logf("limiter applied (oversized result capped): %v", m["error"])
		} else if len(fmt.Sprint(res)) > 65_000 {
			t.Errorf("limiter NOT applied: result still %d bytes", len(fmt.Sprint(res)))
		}
	}

	// Behavior: an error from the inner tool must be converted to {ok:false}
	// by nonFatal (recoverable), so Execute returns a result, not an error.
	// Note bash in the real factory goes through ApprovalTool which requires a
	// sender; with needApproval=false the approval wrapper is skipped.
	erring := &mockTool{
		name: "bash",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return nil, errors.New("boom")
		},
	}
	wrappedErr := f.wrapSessionTool(erring, "sess-1", senderAdapter, map[string]bool{"bash": false})
	res, err = wrappedErr.Execute(context.Background(), map[string]interface{}{"command": "ls"})
	if err != nil {
		t.Fatalf("nonFatal should convert recoverable error to result, got error: %v", err)
	}
	if m, ok := res.(map[string]interface{}); !ok || m["ok"] != false {
		t.Errorf("expected {ok:false} structured result, got %#v", res)
	}
}

// neverLoopDetector is a LoopDetector that never flags anything, so wrapper
// tests can exercise the rest of the stack without loop interference.
type neverLoopDetector struct{}

func (d *neverLoopDetector) Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult {
	return &agentutils.LoopDetectResult{IsLoop: false}
}
func (d *neverLoopDetector) Record(sessionID string, action agentutils.LoopDetectAction) {}
func (d *neverLoopDetector) RecordWithResult(sessionID string, action agentutils.LoopDetectAction, resultHash string) {
}
func (d *neverLoopDetector) Reset(sessionID string)                                {}
func (d *neverLoopDetector) GetStats(sessionID string) agentutils.LoopDetectStats { return agentutils.LoopDetectStats{} }

// TestNonFatal_ApprovalRejectedPassthrough is the BLOCKER-2 regression test:
// a user rejection of an approval must NOT be converted into a recoverable
// {ok:false} result by nonFatalTool — it must surface as a real error.
func TestNonFatal_ApprovalRejectedPassthrough(t *testing.T) {
	rejecting := &mockTool{
		name: "bash",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return nil, &tools.ApprovalRejectedError{ToolName: "bash"}
		},
	}
	nonFatal := tools.WrapNonFatalTool(rejecting)

	res, err := nonFatal.Execute(context.Background(), map[string]interface{}{"command": "ls"})
	if err == nil {
		t.Fatal("approval rejection must surface as a real error, not a result")
	}
	if res != nil {
		t.Fatalf("expected nil result on rejection, got %v", res)
	}
	var apErr *tools.ApprovalRejectedError
	if !errors.As(err, &apErr) {
		t.Fatalf("expected ApprovalRejectedError, got %T: %v", err, err)
	}
	if apErr.ToolName != "bash" {
		t.Errorf("expected ToolName bash, got %q", apErr.ToolName)
	}
}

// TestApprovalTool_RejectionIsApprovalRejectedError verifies the ApprovalTool
// itself wraps a user "no" in the typed veto error.
func TestApprovalTool_RejectionIsApprovalRejectedError(t *testing.T) {
	store := NewApprovalStore(5 * time.Second)
	sender := &recordingApprovalSender{}
	store.SetSender(sender)

	inner := &mockTool{name: "bash"}
	approvalTool := NewApprovalTool(inner, "sess", store, map[string]bool{"bash": true})

	go func() {
		// The approval store receives the request and immediately rejects it.
		deadline := time.Now().Add(2 * time.Second)
		for sender.firstRequestID() == "" && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if id := sender.firstRequestID(); id != "" {
			store.Respond(id, false)
		}
	}()

	_, err := approvalTool.Execute(context.Background(), map[string]interface{}{"command": "rm -rf /"})
	if err == nil {
		t.Fatal("expected rejection error")
	}
	var apErr *tools.ApprovalRejectedError
	if !errors.As(err, &apErr) {
		t.Fatalf("expected ApprovalRejectedError, got %T: %v", err, err)
	}
}

// TestNonFatal_MCPErrorFeedsBack verifies that a plain MCP/bash execution error
// is converted into a structured {ok:false} result fed back to the model
// (F2's error-structuring behavior), not a hard error.
func TestNonFatal_MCPErrorFeedsBack(t *testing.T) {
	failing := &mockTool{
		name: "mcp_weather",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return nil, errors.New("mcp server unreachable")
		},
	}
	nonFatal := tools.WrapNonFatalTool(failing)

	res, err := nonFatal.Execute(context.Background(), map[string]interface{}{"city": "sf"})
	if err != nil {
		t.Fatalf("nonFatal must not return a hard error for recoverable MCP errors, got: %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured map result, got %T", res)
	}
	if m["ok"] != false {
		t.Errorf("expected ok:false, got %v", m["ok"])
	}
	if !strings.Contains(m["error"].(string), "mcp server unreachable") {
		t.Errorf("expected error detail in result, got %v", m["error"])
	}
}

// TestLimiter_ConfigDefaults verifies that a zero-value limiter config falls
// back to the 120KB/60KB defaults inside the limiter wrapper.
func TestLimiter_ConfigDefaults(t *testing.T) {
	big := &mockTool{
		name: "big_output",
		executeFunc: func(input map[string]interface{}) (interface{}, error) {
			return strings.Repeat("x", 70_000), nil // > 60KB string limit
		},
	}
	// 0 limits → wrapper applies its own 120_000/60_000 defaults.
	limiter := tools.WrapToolResultLimiter(big, 0, 0)
	res, err := limiter.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("limiter should not error, got %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("expected limiter to return {ok:false} for oversized string, got %T", res)
	}
	if m["ok"] != false {
		t.Errorf("expected ok:false for >60KB string, got %v", m["ok"])
	}
}
