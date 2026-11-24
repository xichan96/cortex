package dino

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
)

// countingPipeTool is a types.Tool that records how often it executed.
type countingPipeTool struct {
	name string
	mu   sync.Mutex
	call int
}

func (t *countingPipeTool) Name() string                   { return t.name }
func (t *countingPipeTool) Description() string            { return "counting test tool" }
func (t *countingPipeTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *countingPipeTool) Metadata() types.ToolMetadata   { return types.ToolMetadata{} }
func (t *countingPipeTool) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	t.mu.Lock()
	t.call++
	t.mu.Unlock()
	return "executed", nil
}

func (t *countingPipeTool) CallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.call
}

// recordingApprovalBroker records requests and answers with a fixed decision.
type recordingApprovalBroker struct {
	mu       sync.Mutex
	requests []approval.ApprovalRequest
	decision approval.Action
	reason   string
	err      error
}

func (b *recordingApprovalBroker) Request(_ context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	b.mu.Unlock()
	if b.err != nil {
		return approval.ApprovalResponse{}, b.err
	}
	return approval.ApprovalResponse{Decision: b.decision, Scope: approval.ScopeOnce, Reason: b.reason}, nil
}

func (b *recordingApprovalBroker) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requests)
}

func (b *recordingApprovalBroker) LastRequest() approval.ApprovalRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.requests) == 0 {
		return approval.ApprovalRequest{}
	}
	return b.requests[len(b.requests)-1]
}

// capturingApprovalSender records the approval request and answers it with a
// configurable decision. Responding synchronously is safe: ApprovalStore's
// pending channel is buffered and Respond does not hold the store lock while
// sending.
type capturingApprovalSender struct {
	store   *ApprovalStore
	approve bool

	mu        sync.Mutex
	requests  int
	sessionID string
	requestID string
	toolName  string
	arguments string
}

func (s *capturingApprovalSender) SendToolApprovalRequest(sessionID, requestID, toolName, arguments string) {
	s.mu.Lock()
	s.requests++
	s.sessionID, s.requestID, s.toolName, s.arguments = sessionID, requestID, toolName, arguments
	s.mu.Unlock()
	s.store.Respond(requestID, s.approve)
}

func (s *capturingApprovalSender) SendToolApprovalResponse(string, string, string, bool) {}

func (s *capturingApprovalSender) Snapshot() (int, string, string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.sessionID, s.requestID, s.toolName, s.arguments
}

// pipelineTestRuleset builds a ruleset with optional deny/ask tool lists plus
// the same default rules the factory uses.
func pipelineTestRuleset(denied, ask []string) permission.Ruleset {
	return permission.Merge(permission.FromAllowDenyAsk(denied, ask, nil), permission.DefaultRuleset())
}

func newPermissionAuthorizer(denied, ask []string, broker approval.Broker) *permissionAuthorizer {
	return &permissionAuthorizer{
		evaluator: permission.NewEvaluator(pipelineTestRuleset(denied, ask)),
		broker:    broker,
		timeout:   time.Second,
	}
}

func TestPipelineApprovalAllowSkipsBrokerAndExecutes(t *testing.T) {
	tool := &countingPipeTool{name: "allowed_tool"}
	broker := &recordingApprovalBroker{decision: approval.ActionDeny}
	p := pipeline.New(pipeline.Config{Authorizer: newPermissionAuthorizer(nil, nil, broker)})

	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{ID: "c1", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if res.IsError {
		t.Fatalf("allowed tool returned error result: %s", res.Text())
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.CallCount())
	}
	if broker.Calls() != 0 {
		t.Fatalf("broker calls = %d, want 0 for an allowed tool", broker.Calls())
	}
}

func TestPipelineApprovalDenyBlocksWithoutExecuting(t *testing.T) {
	tool := &countingPipeTool{name: "denied_tool"}
	broker := &recordingApprovalBroker{decision: approval.ActionAllow}
	p := pipeline.New(pipeline.Config{Authorizer: newPermissionAuthorizer([]string{"denied_tool"}, nil, broker)})

	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{ID: "c2", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError {
		t.Fatalf("denied tool must return an error result, got %q", res.Text())
	}
	if msg := res.Text(); !strings.Contains(msg, "denied by permission: denied_tool") {
		t.Fatalf("result = %q, want denied-by-permission message", msg)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("denied tool executed %d times, want 0", tool.CallCount())
	}
	if broker.Calls() != 0 {
		t.Fatalf("broker calls = %d, want 0 for a deny rule", broker.Calls())
	}
}

func TestPipelineApprovalAskApprovedExecutesOnce(t *testing.T) {
	tool := &countingPipeTool{name: "ask_tool"}
	broker := &recordingApprovalBroker{decision: approval.ActionAllow}
	p := pipeline.New(pipeline.Config{Authorizer: newPermissionAuthorizer(nil, []string{"ask_tool"}, broker)})

	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{
		ID:        "c3",
		Name:      tool.name,
		Tool:      tool,
		SessionID: "sess-42",
		Args:      map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if res.IsError {
		t.Fatalf("approved tool returned error result: %s", res.Text())
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.CallCount())
	}
	if broker.Calls() != 1 {
		t.Fatalf("broker calls = %d, want 1", broker.Calls())
	}
	req := broker.LastRequest()
	if req.SessionID != "sess-42" || req.ToolName != "ask_tool" {
		t.Fatalf("broker request = %+v, want session sess-42 tool ask_tool", req)
	}
	if req.Args["command"] != "ls" {
		t.Fatalf("broker request args = %+v, want command=ls", req.Args)
	}
}

func TestPipelineApprovalAskDeniedDoesNotExecute(t *testing.T) {
	tool := &countingPipeTool{name: "ask_tool"}
	broker := &recordingApprovalBroker{decision: approval.ActionDeny, reason: "user rejected"}
	p := pipeline.New(pipeline.Config{Authorizer: newPermissionAuthorizer(nil, []string{"ask_tool"}, broker)})

	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{ID: "c4", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError {
		t.Fatalf("rejected tool must return an error result, got %q", res.Text())
	}
	if msg := res.Text(); !strings.Contains(msg, "user rejected") {
		t.Fatalf("result = %q, want the broker's rejection reason", msg)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("rejected tool executed %d times, want 0", tool.CallCount())
	}
	if broker.Calls() != 1 {
		t.Fatalf("broker calls = %d, want 1", broker.Calls())
	}
}

// TestPipelineApprovalWithoutSenderFailsClosed covers the design rule that an
// approval-mode ask with no approval channel must never silently execute.
func TestPipelineApprovalWithoutSenderFailsClosed(t *testing.T) {
	tool := &countingPipeTool{name: "ask_tool"}
	store := NewApprovalStore(time.Second) // no sender installed
	auth := newPermissionAuthorizer(nil, []string{"ask_tool"}, &approvalStoreBroker{store: store})
	p := pipeline.New(pipeline.Config{Authorizer: auth})

	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{ID: "c5", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError {
		t.Fatalf("missing approval channel must fail closed, got %q", res.Text())
	}
	if msg := res.Text(); !strings.Contains(msg, "approval") {
		t.Fatalf("result = %q, want an approval-related error", msg)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("tool executed %d times without an approval channel, want 0", tool.CallCount())
	}
}

func TestApprovalStoreBrokerRoutesSessionToolAndArgs(t *testing.T) {
	store := NewApprovalStore(time.Second)
	sender := &capturingApprovalSender{store: store, approve: true}
	store.SetSender(sender)
	broker := &approvalStoreBroker{store: store}

	resp, err := broker.Request(context.Background(), approval.ApprovalRequest{
		SessionID: "sess-42",
		ToolName:  "Bash",
		Args:      map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !resp.Allowed() {
		t.Fatalf("response = %+v, want allow", resp)
	}

	n, sessionID, requestID, toolName, arguments := sender.Snapshot()
	if n != 1 || sessionID != "sess-42" || toolName != "Bash" || requestID == "" {
		t.Fatalf("sender got requests=%d session=%q request=%q tool=%q", n, sessionID, requestID, toolName)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		t.Fatalf("arguments are not JSON: %v (%q)", err, arguments)
	}
	if decoded["command"] != "ls" {
		t.Fatalf("arguments = %q, want command=ls", arguments)
	}
}

func TestApprovalStoreBrokerDenyIsNotAnError(t *testing.T) {
	store := NewApprovalStore(time.Second)
	sender := &capturingApprovalSender{store: store, approve: false}
	store.SetSender(sender)
	broker := &approvalStoreBroker{store: store}

	resp, err := broker.Request(context.Background(), approval.ApprovalRequest{
		SessionID: "sess-42",
		ToolName:  "Bash",
	})
	if err != nil {
		t.Fatalf("a user denial must be a response, not an error: %v", err)
	}
	if resp.Allowed() {
		t.Fatalf("response = %+v, want deny", resp)
	}
}

func TestApprovalStoreBrokerMissingStoreIsUnavailable(t *testing.T) {
	_, err := (&approvalStoreBroker{}).Request(context.Background(), approval.ApprovalRequest{ToolName: "Bash"})
	if err == nil {
		t.Fatal("missing ApprovalStore must return an error")
	}
}

// TestWrapSessionToolSkipsApprovalToolForPipelineApproval proves the pipeline
// is the single approval gate: with approval mode active, the tool wrapper
// stack must not contain the legacy ApprovalTool (otherwise the user would be
// prompted twice).
func TestWrapSessionToolSkipsApprovalToolForPipelineApproval(t *testing.T) {
	df := newPipelineApprovalFactory(t, ToolPipelineAuthorizerApproval)
	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	inner := &countingPipeTool{name: "needs_approval"}
	wrapped := df.wrapSessionTool(inner, "sess-1", &toolEventSenderAdapter{}, map[string]bool{"needs_approval": true})

	res, err := wrapped.Execute(context.Background(), map[string]interface{}{"command": "ls"})
	if err != nil {
		t.Fatalf("wrapped tool: %v", err)
	}
	if res != "executed" {
		t.Fatalf("result = %v, want executed", res)
	}
	if inner.CallCount() != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.CallCount())
	}
	if n, _, _, _, _ := sender.Snapshot(); n != 0 {
		t.Fatalf("legacy approval wrapper still active (%d requests); pipeline owns approval", n)
	}
	if !df.pipelineHandlesApproval() {
		t.Fatal("pipelineHandlesApproval() = false, want true")
	}
}

// TestWrapSessionToolKeepsApprovalToolWithoutPipeline is the contrast case: the
// legacy path must keep prompting.
func TestWrapSessionToolKeepsApprovalToolWithoutPipeline(t *testing.T) {
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	raw, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	df := raw.(*dinoFactory)
	t.Cleanup(func() {
		df.CloseAll()
		_ = df.Shutdown(context.Background())
	})
	if df.pipelineHandlesApproval() {
		t.Fatal("pipelineHandlesApproval() = true without a pipeline")
	}

	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	inner := &countingPipeTool{name: "needs_approval"}
	wrapped := df.wrapSessionTool(inner, "sess-1", &toolEventSenderAdapter{}, map[string]bool{"needs_approval": true})

	if _, err := wrapped.Execute(context.Background(), map[string]interface{}{"command": "ls"}); err != nil {
		t.Fatalf("wrapped tool: %v", err)
	}
	if inner.CallCount() != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.CallCount())
	}
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1 on the legacy path", n)
	}
}

// TestNewDinoFactoryApprovalAuthorizerDeniesTool verifies that NewDinoFactory
// actually installs the permission authorizer when configured, using the same
// ruleset as the session tool filter.
func TestNewDinoFactoryApprovalAuthorizerDeniesTool(t *testing.T) {
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	cfg.Tools.Denied = []string{"blocked_tool"}
	cfg.ToolPipeline.Enabled = true
	cfg.ToolPipeline.Authorizer = ToolPipelineAuthorizerApproval

	raw, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	df := raw.(*dinoFactory)
	t.Cleanup(func() {
		df.CloseAll()
		_ = df.Shutdown(context.Background())
	})

	p := df.GetToolPipeline()
	if p == nil {
		t.Fatal("pipeline not installed")
	}
	tool := &countingPipeTool{name: "blocked_tool"}
	res, err := p.ExecuteOne(context.Background(), &pipeline.Call{ID: "c1", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "denied by permission: blocked_tool") {
		t.Fatalf("result = %q (isError=%v), want denied by permission", res.Text(), res.IsError)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("blocked tool executed %d times, want 0", tool.CallCount())
	}
}

// TestNewDinoFactoryDefaultAuthorizerAllowsTool pins the backward-compatible
// default: an enabled pipeline without an authorizer setting stays allow_all.
func TestNewDinoFactoryDefaultAuthorizerAllowsTool(t *testing.T) {
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline.Enabled = true

	raw, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	df := raw.(*dinoFactory)
	t.Cleanup(func() {
		df.CloseAll()
		_ = df.Shutdown(context.Background())
	})
	if df.pipelineHandlesApproval() {
		t.Fatal("default must not be approval mode")
	}

	tool := &countingPipeTool{name: "free_tool"}
	res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{ID: "c1", Name: tool.name, Tool: tool})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if res.IsError {
		t.Fatalf("default authorizer blocked the tool: %s", res.Text())
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.CallCount())
	}
}

// TestCreateSessionBindsEngineSessionID verifies the engine carries the
// session id into pipeline calls, so the approval broker can route them.
func TestCreateSessionBindsEngineSessionID(t *testing.T) {
	df := newPipelineApprovalFactory(t, ToolPipelineAuthorizerApproval)
	sess, err := df.CreateSession(context.Background(), "sess-xyz")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := sess.GetAgent().SessionID(); got != "sess-xyz" {
		t.Fatalf("engine SessionID = %q, want sess-xyz", got)
	}
}

func newPipelineApprovalFactory(t *testing.T, authorizer string) *dinoFactory {
	t.Helper()
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline.Enabled = true
	cfg.ToolPipeline.Authorizer = authorizer
	raw, err := NewDinoFactory(cfg)
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
