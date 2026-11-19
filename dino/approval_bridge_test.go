package dino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/pipeline"
)

// fakeApprovalBridge is a Bridge that answers every request with a fixed
// decision and records the request it received. Auto allow/deny is enough for
// the pipeline tests; request mapping is asserted from LastRequest.
type fakeApprovalBridge struct {
	mu        sync.Mutex
	requests  []approval.BridgeRequest
	approved  bool
	cancelled bool
	err       error
}

func (b *fakeApprovalBridge) Request(_ context.Context, req approval.BridgeRequest) (approval.BridgeResponse, error) {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	b.mu.Unlock()
	if b.err != nil {
		return approval.BridgeResponse{ID: req.ID}, b.err
	}
	return approval.BridgeResponse{ID: req.ID, Approved: b.approved, Cancelled: b.cancelled}, nil
}

func (b *fakeApprovalBridge) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requests)
}

func (b *fakeApprovalBridge) LastRequest() approval.BridgeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.requests) == 0 {
		return approval.BridgeRequest{}
	}
	return b.requests[len(b.requests)-1]
}

func TestBridgeBrokerMapsRequestFields(t *testing.T) {
	bridge := &fakeApprovalBridge{approved: true}
	broker := &bridgeBroker{bridge: bridge}

	resp, err := broker.Request(context.Background(), approval.ApprovalRequest{
		ID:         "call-42",
		SessionID:  "sess-7",
		TurnID:     "turn-3",
		ToolCallID: "call-42",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls -la"},
		Timeout:    90 * time.Second,
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !resp.Allowed() {
		t.Fatalf("response = %+v, want allow", resp)
	}

	if bridge.Calls() != 1 {
		t.Fatalf("bridge calls = %d, want 1", bridge.Calls())
	}
	req := bridge.LastRequest()
	if req.Kind != approval.KindApproval {
		t.Fatalf("kind = %q, want %q", req.Kind, approval.KindApproval)
	}
	if req.ID != "call-42" {
		t.Fatalf("id = %q, want the tool-call id call-42", req.ID)
	}
	if req.Title != "bash" {
		t.Fatalf("title = %q, want the tool name bash", req.Title)
	}
	if req.SessionID != "sess-7" || req.TurnID != "turn-3" {
		t.Fatalf("session/turn = %q/%q, want sess-7/turn-3", req.SessionID, req.TurnID)
	}
	if req.Timeout != 90*time.Second {
		t.Fatalf("timeout = %v, want 90s", req.Timeout)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (%q)", err, req.Payload)
	}
	if payload["command"] != "ls -la" {
		t.Fatalf("payload = %q, want command=ls -la", req.Payload)
	}
}

func TestBridgeBrokerDecisions(t *testing.T) {
	tests := []struct {
		name      string
		approved  bool
		cancelled bool
		wantAllow bool
		wantErr   bool
		reason    string
	}{
		{name: "approved allows", approved: true, wantAllow: true},
		{name: "rejected denies", approved: false, reason: "approval denied"},
		{name: "cancelled denies", cancelled: true, reason: "approval cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			broker := &bridgeBroker{bridge: &fakeApprovalBridge{approved: tc.approved, cancelled: tc.cancelled}}
			resp, err := broker.Request(context.Background(), approval.ApprovalRequest{ToolName: "bash"})
			if err != nil {
				t.Fatalf("a bridge decision must not become an error: %v", err)
			}
			if resp.Allowed() != tc.wantAllow {
				t.Fatalf("allowed = %v, want %v (resp=%+v)", resp.Allowed(), tc.wantAllow, resp)
			}
			if !tc.wantAllow && resp.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", resp.Reason, tc.reason)
			}
		})
	}
}

func TestBridgeBrokerReturnsBridgeErrorUnchanged(t *testing.T) {
	wantErr := errors.New("ui disconnected")
	broker := &bridgeBroker{bridge: &fakeApprovalBridge{err: wantErr}}

	_, err := broker.Request(context.Background(), approval.ApprovalRequest{ToolName: "bash"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the bridge error unchanged", err)
	}
}

// bridgeFunc adapts a function to approval.Bridge for one-off test cases.
type bridgeFunc func(context.Context, approval.BridgeRequest) (approval.BridgeResponse, error)

func (f bridgeFunc) Request(ctx context.Context, req approval.BridgeRequest) (approval.BridgeResponse, error) {
	return f(ctx, req)
}

func TestBridgeBrokerResponseErrorIsAnError(t *testing.T) {
	broker := &bridgeBroker{bridge: bridgeFunc(func(context.Context, approval.BridgeRequest) (approval.BridgeResponse, error) {
		return approval.BridgeResponse{Error: "render failed"}, nil
	})}
	_, err := broker.Request(context.Background(), approval.ApprovalRequest{ToolName: "bash"})
	if err == nil || !strings.Contains(err.Error(), "render failed") {
		t.Fatalf("err = %v, want the UI error surfaced", err)
	}
}

func TestBridgeBrokerMissingBridgeIsUnavailable(t *testing.T) {
	_, err := (&bridgeBroker{}).Request(context.Background(), approval.ApprovalRequest{ToolName: "bash"})
	if !errors.Is(err, approval.ErrApprovalUnavailable) {
		t.Fatalf("err = %v, want ErrApprovalUnavailable", err)
	}
}

func TestBridgeBrokerUnmarshalableArgsFailsClosed(t *testing.T) {
	bridge := &fakeApprovalBridge{approved: true}
	broker := &bridgeBroker{bridge: bridge}

	_, err := broker.Request(context.Background(), approval.ApprovalRequest{
		ToolName: "bash",
		Args:     map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("unmarshalable args must return an error")
	}
	if bridge.Calls() != 0 {
		t.Fatalf("bridge calls = %d, want 0 when the payload cannot be built", bridge.Calls())
	}
}

// newBridgePipelineFactory builds an approval-mode factory whose ask rule
// names ask_tool, mirroring the wiring verified in pipeline_approval_test.go.
func newBridgePipelineFactory(t *testing.T, opts ...FactoryOption) *dinoFactory {
	t.Helper()
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline.Enabled = true
	cfg.ToolPipeline.Authorizer = ToolPipelineAuthorizerApproval
	cfg.Tools.ApprovalRequired = []string{"ask_tool"}

	raw, err := NewDinoFactory(cfg, opts...)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	df := raw.(*dinoFactory)
	t.Cleanup(func() {
		df.CloseAll()
		_ = df.Shutdown(context.Background())
	})
	return df
}

func TestNewDinoFactoryWithUIBridgeRoutesAskDecisions(t *testing.T) {
	tests := []struct {
		name      string
		approved  bool
		wantRuns  int
		wantError bool
	}{
		{name: "allow executes", approved: true, wantRuns: 1},
		{name: "deny blocks", approved: false, wantRuns: 0, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &fakeApprovalBridge{approved: tc.approved}
			df := newBridgePipelineFactory(t, WithUIBridge(bridge))

			p := df.GetToolPipeline()
			if p == nil {
				t.Fatal("GetToolPipeline() = nil, want an approval pipeline")
			}
			tool := &countingPipeTool{name: "ask_tool"}
			res, err := p.ExecuteOne(context.Background(), &pipeline.Call{
				ID:        "call-42",
				Name:      "ask_tool",
				Tool:      tool,
				SessionID: "sess-bridge",
				TurnID:    "turn-7",
				Args:      map[string]any{"command": "ls"},
			})
			if err != nil {
				t.Fatalf("ExecuteOne: %v", err)
			}
			if res.IsError != tc.wantError {
				t.Fatalf("result = %q (isError=%v), want isError=%v", res.Text(), res.IsError, tc.wantError)
			}
			if tool.CallCount() != tc.wantRuns {
				t.Fatalf("tool calls = %d, want %d", tool.CallCount(), tc.wantRuns)
			}
			if bridge.Calls() != 1 {
				t.Fatalf("bridge calls = %d, want 1", bridge.Calls())
			}
			req := bridge.LastRequest()
			if req.Kind != approval.KindApproval || req.Title != "ask_tool" {
				t.Fatalf("bridge request = %+v, want kind=approval title=ask_tool", req)
			}
			if req.SessionID != "sess-bridge" || req.TurnID != "turn-7" {
				t.Fatalf("bridge routing = %q/%q, want sess-bridge/turn-7", req.SessionID, req.TurnID)
			}
		})
	}
}

// TestNewDinoFactoryWithUIBridgeErrorFailsClosed pins the fail-closed rule: a
// broken bridge must block execution, not fall through to another channel.
func TestNewDinoFactoryWithUIBridgeErrorFailsClosed(t *testing.T) {
	bridge := &fakeApprovalBridge{err: errors.New("ui disconnected")}
	df := newBridgePipelineFactory(t, WithUIBridge(bridge))

	tool := &countingPipeTool{name: "ask_tool"}
	res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:   "call-err",
		Name: "ask_tool",
		Tool: tool,
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "ui disconnected") {
		t.Fatalf("result = %q (isError=%v), want a fail-closed authorization error", res.Text(), res.IsError)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("tool executed %d times with a broken bridge, want 0", tool.CallCount())
	}
	if bridge.Calls() != 1 {
		t.Fatalf("bridge calls = %d, want 1", bridge.Calls())
	}
}

// TestNewDinoFactoryWithoutUIBridgeKeepsApprovalStore is the compatibility
// case: no bridge means the ApprovalStore sender still answers ask decisions.
func TestNewDinoFactoryWithoutUIBridgeKeepsApprovalStore(t *testing.T) {
	df := newBridgePipelineFactory(t)
	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	tool := &countingPipeTool{name: "ask_tool"}
	res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:        "call-store",
		Name:      "ask_tool",
		Tool:      tool,
		SessionID: "sess-store",
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if res.IsError {
		t.Fatalf("approved tool returned an error result: %s", res.Text())
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.CallCount())
	}
	if n, sessionID, _, toolName, _ := sender.Snapshot(); n != 1 || sessionID != "sess-store" || toolName != "ask_tool" {
		t.Fatalf("ApprovalStore sender got requests=%d session=%q tool=%q, want 1/sess-store/ask_tool", n, sessionID, toolName)
	}
}

// TestWithUIBridgeLeavesNonApprovalPipelinesAlone covers the compatibility
// rules: the option must not route anything when the pipeline is disabled or
// uses the allow_all authorizer.
func TestWithUIBridgeLeavesNonApprovalPipelinesAlone(t *testing.T) {
	t.Run("pipeline disabled", func(t *testing.T) {
		cfg := getTestConfig()
		cfg.Provider.Type = "mock"
		bridge := &fakeApprovalBridge{approved: true}

		raw, err := NewDinoFactory(cfg, WithUIBridge(bridge))
		if err != nil {
			t.Fatalf("NewDinoFactory: %v", err)
		}
		df := raw.(*dinoFactory)
		t.Cleanup(func() {
			df.CloseAll()
			_ = df.Shutdown(context.Background())
		})
		if df.GetToolPipeline() != nil {
			t.Fatal("pipeline = non-nil with ToolPipeline.Enabled=false")
		}
		if bridge.Calls() != 0 {
			t.Fatalf("bridge calls = %d, want 0", bridge.Calls())
		}
	})

	t.Run("allow_all authorizer", func(t *testing.T) {
		cfg := getTestConfig()
		cfg.Provider.Type = "mock"
		cfg.ToolPipeline.Enabled = true
		bridge := &fakeApprovalBridge{approved: false}

		raw, err := NewDinoFactory(cfg, WithUIBridge(bridge))
		if err != nil {
			t.Fatalf("NewDinoFactory: %v", err)
		}
		df := raw.(*dinoFactory)
		t.Cleanup(func() {
			df.CloseAll()
			_ = df.Shutdown(context.Background())
		})
		tool := &countingPipeTool{name: "free_tool"}
		res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
			ID:   "call-free",
			Name: "free_tool",
			Tool: tool,
		})
		if err != nil {
			t.Fatalf("ExecuteOne: %v", err)
		}
		if res.IsError {
			t.Fatalf("allow_all pipeline blocked the tool: %s", res.Text())
		}
		if tool.CallCount() != 1 || bridge.Calls() != 0 {
			t.Fatalf("tool calls = %d bridge calls = %d, want 1/0", tool.CallCount(), bridge.Calls())
		}
	})
}
