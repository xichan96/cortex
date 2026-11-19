package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/types"
)

// --- test doubles ---

type mockTool struct {
	name   string
	schema map[string]interface{}
	exec   func(ctx context.Context, args map[string]interface{}) (interface{}, error)
	meta   types.ToolMetadata
}

func newTool(name string, exec func(context.Context, map[string]interface{}) (interface{}, error)) *mockTool {
	return &mockTool{name: name, exec: exec}
}

func (t *mockTool) Name() string                   { return t.name }
func (t *mockTool) Description() string            { return t.name }
func (t *mockTool) Schema() map[string]interface{} { return t.schema }
func (t *mockTool) Metadata() types.ToolMetadata   { return t.meta }
func (t *mockTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.exec == nil {
		return "ok", nil
	}
	return t.exec(ctx, args)
}

type preparingTool struct {
	*mockTool
	prepare func(map[string]interface{}) (map[string]interface{}, error)
}

func (t *preparingTool) PrepareArguments(args map[string]interface{}) (map[string]interface{}, error) {
	return t.prepare(args)
}

type modeTool struct {
	*mockTool
	mode types.ExecutionMode
}

func (t *modeTool) ExecutionMode() types.ExecutionMode { return t.mode }

type riskTool struct {
	*mockTool
	risk types.RiskLevel
}

func (t *riskTool) Risk() types.RiskLevel { return t.risk }

type stageFunc struct {
	name   string
	before func(context.Context, *Call) (Decision, error)
	after  func(context.Context, *Call, *Result) error
}

func (s stageFunc) Name() string { return s.name }
func (s stageFunc) Before(ctx context.Context, c *Call) (Decision, error) {
	if s.before == nil {
		return Continue(), nil
	}
	return s.before(ctx, c)
}
func (s stageFunc) After(ctx context.Context, c *Call, r *Result) error {
	if s.after == nil {
		return nil
	}
	return s.after(ctx, c, r)
}

func testCall(id, name string, tool types.Tool, args map[string]interface{}) *Call {
	return &Call{ID: id, Name: name, Tool: tool, Args: args, SessionID: "s1", TurnID: "t1"}
}

func newTestPipeline(t *testing.T, cfg Config) *Pipeline {
	t.Helper()
	if cfg.Authorizer == nil {
		cfg.Authorizer = AllowAllAuthorizer{}
	}
	return New(cfg)
}

// --- allow / deny / fatal / recoverable ---

func TestExecuteOneAllows(t *testing.T) {
	tool := newTool("echo", func(_ context.Context, args map[string]interface{}) (interface{}, error) {
		return "hello " + args["x"].(string), nil
	})
	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "echo", tool, map[string]interface{}{"x": "world"}))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if r.IsError {
		t.Fatalf("expected success, got error: %s", r.Text())
	}
	if got := r.Text(); got != "hello world" {
		t.Fatalf("text = %q", got)
	}
}

func TestExecuteOneDeniedByPolicy(t *testing.T) {
	tool := newTool("danger", nil)
	p := New(Config{Authorizer: DenyAllAuthorizer{Reason: "not allowed"}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "danger", tool, nil))
	if err != nil {
		t.Fatalf("denial must be recoverable, got fatal: %v", err)
	}
	if !r.IsError {
		t.Fatal("expected error result")
	}
	if got := r.Text(); got != "blocked by policy: not allowed" {
		t.Fatalf("text = %q", got)
	}
}

func TestDefaultAuthorizerIsFailClosed(t *testing.T) {
	tool := newTool("danger", func(context.Context, map[string]interface{}) (interface{}, error) {
		t.Fatal("tool must not execute without an authorizer")
		return nil, nil
	})
	p := New(Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "danger", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "no authorizer configured") {
		t.Fatalf("expected fail-closed denial, got %q", r.Text())
	}
}

func TestAuthorizationErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{approval.ErrApprovalTimeout, "approval timed out"},
		{context.DeadlineExceeded, "approval timed out"},
		{approval.ErrApprovalCancelled, "approval cancelled"},
		{context.Canceled, "approval cancelled"},
		{approval.ErrApprovalDenied, "blocked by policy: approval denied"},
		{errors.New("boom"), "authorization failed: boom"},
	}
	for _, tc := range cases {
		p := New(Config{Authorizer: AuthorizerFunc(func(context.Context, *Call) (Decision, error) {
			return Decision{}, tc.err
		})})
		r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", newTool("t", nil), nil))
		if err != nil {
			t.Fatalf("%v: unexpected fatal: %v", tc.err, err)
		}
		if !r.IsError || r.Text() != tc.want {
			t.Errorf("%v: got %q, want %q", tc.err, r.Text(), tc.want)
		}
	}
}

func TestFatalErrorPropagates(t *testing.T) {
	fatal := &types.FatalToolError{Reason: "unrecoverable"}
	tool := newTool("fatal", func(context.Context, map[string]interface{}) (interface{}, error) {
		return nil, fatal
	})
	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "fatal", tool, nil))
	if err == nil {
		t.Fatal("expected fatal error to propagate")
	}
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want the fatal error", err)
	}
	if r == nil || !r.IsError {
		t.Fatalf("expected an error result alongside the fatal error, got %+v", r)
	}
}

func TestFatalErrorFromValidationPropagates(t *testing.T) {
	fatal := &types.FatalToolError{Reason: "schema is unusable"}
	p := New(Config{
		Authorizer: AllowAllAuthorizer{},
		Validator: ValidatorFunc(func(context.Context, *Call) error {
			return fatal
		}),
	})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", newTool("t", nil), nil))
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want fatal validator error", err)
	}
	if r == nil || !r.IsError {
		t.Fatalf("fatal stage must still produce an error result, got %+v", r)
	}
}

func TestRecoverableErrorIsResultAndRetried(t *testing.T) {
	var calls atomic.Int32
	tool := newTool("flaky", func(context.Context, map[string]interface{}) (interface{}, error) {
		calls.Add(1)
		return nil, errors.New("temporary")
	})
	p := newTestPipeline(t, Config{Limits: Limits{Retries: 1}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "flaky", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "temporary") {
		t.Fatalf("got %q", r.Text())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2 (initial + 1 retry)", got)
	}
}

func TestPerToolTimeout(t *testing.T) {
	tool := newTool("slow", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := newTestPipeline(t, Config{Limits: Limits{PerToolTimeout: map[string]time.Duration{"slow": 20 * time.Millisecond}}})
	start := time.Now()
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "slow", tool, nil))
	if err != nil {
		t.Fatalf("timeout must be a result: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "deadline exceeded") {
		t.Fatalf("got %q", r.Text())
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
}

// --- resolve / prepare / validate / hooks ---

func TestUnknownToolBecomesResult(t *testing.T) {
	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), &Call{ID: "c1", Name: "nope"})
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || r.Text() != "unknown tool: nope" {
		t.Fatalf("got %q", r.Text())
	}
}

func TestRegistryResolvesTool(t *testing.T) {
	tool := newTool("echo", nil)
	p := newTestPipeline(t, Config{Registry: RegistryFunc(func(name string) (types.Tool, bool) {
		return tool, name == "echo"
	})})
	r, err := p.ExecuteOne(context.Background(), &Call{ID: "c1", Name: "echo", Args: map[string]interface{}{}})
	if err != nil || r.IsError {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestHiddenToolRequiresInternalCall(t *testing.T) {
	tool := newTool("secret", nil)
	tool.meta.Exposure = types.ExposureHidden

	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "secret", tool, nil))
	if err != nil || !r.IsError || !strings.Contains(r.Text(), "not available") {
		t.Fatalf("model call to hidden tool: r=%+v err=%v", r, err)
	}

	c := testCall("c2", "secret", tool, nil)
	c.Internal = true
	r, err = p.ExecuteOne(context.Background(), c)
	if err != nil || r.IsError {
		t.Fatalf("internal hidden call: r=%+v err=%v", r, err)
	}
}

func TestRawArgsDecodedAndPrepareArgumentsApplied(t *testing.T) {
	var seen map[string]interface{}
	tool := &preparingTool{
		mockTool: newTool("edit", func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			seen = args
			return "done", nil
		}),
		prepare: func(args map[string]interface{}) (map[string]interface{}, error) {
			// Legacy single-edit form -> edits[].
			if old, ok := args["oldText"].(string); ok {
				return map[string]interface{}{"edits": []interface{}{map[string]interface{}{"old": old}}}, nil
			}
			return args, nil
		},
	}
	p := newTestPipeline(t, Config{})
	c := &Call{ID: "c1", Name: "edit", Tool: tool, RawArgs: json.RawMessage(`{"oldText":"x"}`)}
	r, err := p.ExecuteOne(context.Background(), c)
	if err != nil || r.IsError {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if _, ok := seen["edits"]; !ok {
		t.Fatalf("prepareArguments result not passed through: %#v", seen)
	}
}

func TestPrepareArgumentsErrorIsRecoverable(t *testing.T) {
	tool := &preparingTool{
		mockTool: newTool("edit", func(context.Context, map[string]interface{}) (interface{}, error) {
			t.Fatal("must not execute")
			return nil, nil
		}),
		prepare: func(map[string]interface{}) (map[string]interface{}, error) {
			return nil, errors.New("bad legacy args")
		},
	}
	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "edit", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "bad legacy args") {
		t.Fatalf("got %q", r.Text())
	}
}

func requiredPathSchema() map[string]interface{} {
	return map[string]interface{}{
		"required": []interface{}{"path"},
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
	}
}

func TestValidationFailureIsRecoverable(t *testing.T) {
	tool := newTool("read", nil)
	tool.schema = requiredPathSchema()
	p := newTestPipeline(t, Config{})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "read", tool, map[string]interface{}{}))
	if err != nil {
		t.Fatalf("validation must not be fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "path") {
		t.Fatalf("got %q", r.Text())
	}
}

func TestStageOrderAuthorizeBeforeHooks(t *testing.T) {
	var order []string
	tool := newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
		order = append(order, "execute")
		return "ok", nil
	})
	hook := stageFunc{name: "h", before: func(_ context.Context, c *Call) (Decision, error) {
		order = append(order, "before")
		if _, ok := c.Args["x"]; !ok {
			t.Errorf("before hook saw unprepared args: %#v", c.Args)
		}
		return Continue(), nil
	}}
	p := New(Config{
		Authorizer: AuthorizerFunc(func(_ context.Context, c *Call) (Decision, error) {
			order = append(order, "authorize")
			if _, ok := c.Args["x"]; !ok {
				t.Errorf("authorizer saw unprepared args: %#v", c.Args)
			}
			return Continue(), nil
		}),
		BeforeHooks: []BeforeStage{hook},
	})
	if _, err := p.ExecuteOne(context.Background(), testCall("c1", "t", tool, map[string]interface{}{"x": "y"})); err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	want := []string{"authorize", "before", "execute"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestBeforeHookRewriteIsRevalidated(t *testing.T) {
	tool := newTool("read", func(context.Context, map[string]interface{}) (interface{}, error) {
		t.Fatal("must not execute with invalid rewritten args")
		return nil, nil
	})
	tool.schema = requiredPathSchema()
	hook := stageFunc{name: "rewrite", before: func(_ context.Context, c *Call) (Decision, error) {
		return Decision{Action: ActionContinue, Args: map[string]interface{}{}}, nil
	}}
	p := newTestPipeline(t, Config{BeforeHooks: []BeforeStage{hook}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "read", tool, map[string]interface{}{"path": "ok"}))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "path") {
		t.Fatalf("expected revalidation to reject rewritten args, got %q", r.Text())
	}
}

func TestBeforeHookErrorIsFailClosed(t *testing.T) {
	tool := newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
		t.Fatal("must not execute")
		return nil, nil
	})
	hook := stageFunc{name: "boom", before: func(context.Context, *Call) (Decision, error) {
		return Decision{}, errors.New("hook exploded")
	}}
	p := newTestPipeline(t, Config{BeforeHooks: []BeforeStage{hook}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "hook exploded") {
		t.Fatalf("got %q", r.Text())
	}
}

func TestAfterHookModifiesResult(t *testing.T) {
	tool := newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "body", nil
	})
	hook := stageFunc{name: "annotate", after: func(_ context.Context, _ *Call, r *Result) error {
		r.AppendText(" [annotated]")
		return nil
	}}
	p := newTestPipeline(t, Config{AfterHooks: []AfterStage{hook}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if got := r.Text(); got != "body [annotated]" {
		t.Fatalf("text = %q", got)
	}
}

func TestAfterHookErrorReplacesResult(t *testing.T) {
	tool := newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "body", nil
	})
	hook := stageFunc{name: "boom", after: func(context.Context, *Call, *Result) error {
		return errors.New("after failed")
	}}
	p := newTestPipeline(t, Config{AfterHooks: []AfterStage{hook}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || !strings.Contains(r.Text(), "after failed") {
		t.Fatalf("got %q", r.Text())
	}
}

// --- events / record ---

func TestEventsAndRecorder(t *testing.T) {
	tool := newTool("t", nil)
	var mu sync.Mutex
	var events []Event
	var recorded int
	p := newTestPipeline(t, Config{
		EventSink: EventSinkFunc(func(_ context.Context, ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}),
		Recorder: RecorderFunc(func(context.Context, *Call, *Result) error {
			recorded++
			return errors.New("recorder failure must not block")
		}),
	})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", tool, nil))
	if err != nil || r.IsError {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if recorded != 1 {
		t.Fatalf("recorded = %d, want 1", recorded)
	}
	if len(events) != 2 || events[0].Type != EventToolExecutionStart || events[1].Type != EventToolExecutionEnd {
		t.Fatalf("events = %+v", events)
	}
	if events[1].CallID != "c1" || events[1].Result == nil {
		t.Fatalf("end event missing data: %+v", events[1])
	}
}

// --- broker / approval integration ---

func TestBrokerAuthorizerAllowAndDeny(t *testing.T) {
	tool := newTool("t", nil)

	allow := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: approval.AllowBroker{}}})
	r, err := allow.ExecuteOne(context.Background(), testCall("c1", "t", tool, nil))
	if err != nil || r.IsError {
		t.Fatalf("allow: r=%+v err=%v", r, err)
	}

	deny := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: approval.DenyBroker{Reason: "nope"}}})
	r, err = deny.ExecuteOne(context.Background(), testCall("c2", "t", tool, nil))
	if err != nil {
		t.Fatalf("deny: unexpected fatal: %v", err)
	}
	if !r.IsError || r.Text() != "blocked by policy: nope" {
		t.Fatalf("deny: got %q", r.Text())
	}
}

func TestBrokerAuthorizerTimeout(t *testing.T) {
	broker := approval.FuncBroker(func(ctx context.Context, _ approval.ApprovalRequest) (approval.ApprovalResponse, error) {
		<-ctx.Done()
		return approval.ApprovalResponse{}, ctx.Err()
	})
	p := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: broker, Timeout: 20 * time.Millisecond}})
	start := time.Now()
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", newTool("t", nil), nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || r.Text() != "approval timed out" {
		t.Fatalf("got %q", r.Text())
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took too long: %v", time.Since(start))
	}
}

func TestBrokerAuthorizerCancel(t *testing.T) {
	broker := approval.FuncBroker(func(ctx context.Context, _ approval.ApprovalRequest) (approval.ApprovalResponse, error) {
		<-ctx.Done()
		return approval.ApprovalResponse{}, ctx.Err()
	})
	p := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: broker}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	r, err := p.ExecuteOne(ctx, testCall("c1", "t", newTool("t", nil), nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || r.Text() != "approval cancelled" {
		t.Fatalf("got %q", r.Text())
	}
}

func TestBrokerAuthorizerNoBrokerIsFailClosed(t *testing.T) {
	p := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "t", newTool("t", nil), nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if !r.IsError || r.Text() != "blocked by policy: approval broker unavailable" {
		t.Fatalf("got %q", r.Text())
	}
}

func TestBrokerAuthorizerRemembersSessionScope(t *testing.T) {
	var asks atomic.Int32
	broker := approval.FuncBroker(func(context.Context, approval.ApprovalRequest) (approval.ApprovalResponse, error) {
		asks.Add(1)
		return approval.ApprovalResponse{Decision: approval.ActionAllow, Scope: approval.ScopeSession, Remember: true}, nil
	})
	store := approval.NewScopeStore(approval.NewMemoryStore(), nil, nil)
	p := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: broker, Store: store}})

	for i := 0; i < 3; i++ {
		c := testCall("c"+string(rune('0'+i)), "t", newTool("t", nil), nil)
		if _, err := p.ExecuteOne(context.Background(), c); err != nil {
			t.Fatalf("unexpected fatal: %v", err)
		}
	}
	if got := asks.Load(); got != 1 {
		t.Fatalf("broker asked %d times, want 1 (remembered)", got)
	}
}

func TestBrokerAuthorizerHighRiskNarrowsScopes(t *testing.T) {
	var gotScopes []approval.ApprovalScope
	broker := approval.FuncBroker(func(_ context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
		gotScopes = req.Scopes
		return approval.ApprovalResponse{Decision: approval.ActionAllow}, nil
	})
	tool := &riskTool{mockTool: newTool("rm", nil), risk: types.RiskHigh}
	p := newTestPipeline(t, Config{Authorizer: BrokerAuthorizer{Broker: broker}})
	if _, err := p.ExecuteOne(context.Background(), testCall("c1", "rm", tool, nil)); err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if len(gotScopes) != 2 || gotScopes[0] != approval.ScopeOnce || gotScopes[1] != approval.ScopeSession {
		t.Fatalf("scopes = %v, want [once session]", gotScopes)
	}
}

func TestToolRiskAndExecutionModeHelpers(t *testing.T) {
	meta := newTool("m", nil)
	meta.meta.Risk = types.RiskMedium
	meta.meta.ExecutionMode = types.ExecutionModeSequential
	if types.ToolRisk(meta) != types.RiskMedium {
		t.Fatalf("metadata risk not used")
	}
	if !types.ToolExecutionMode(meta).IsSequential() {
		t.Fatalf("metadata execution mode not used")
	}

	over := &riskTool{mockTool: newTool("r", nil), risk: types.RiskHigh}
	over.meta.Risk = types.RiskLow
	if types.ToolRisk(over) != types.RiskHigh {
		t.Fatalf("interface risk should take precedence")
	}
}
