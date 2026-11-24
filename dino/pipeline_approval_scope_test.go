package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/home"
	"github.com/xichan96/cortex/agent/pipeline"
)

// TestToolPipelineEffectiveApprovalScope pins the config normalization:
// once is the default and unknown values never widen to a persistent scope.
func TestToolPipelineEffectiveApprovalScope(t *testing.T) {
	cases := map[string]approval.ApprovalScope{
		"":          approval.ScopeOnce,
		"once":      approval.ScopeOnce,
		"session":   approval.ScopeSession,
		"workspace": approval.ScopeWorkspace,
		"always":    approval.ScopeAlways,
		"bogus":     approval.ScopeOnce,
	}
	for raw, want := range cases {
		cfg := ToolPipelineConfig{ApprovalScope: raw}
		if got := cfg.EffectiveApprovalScope(); got != want {
			t.Fatalf("EffectiveApprovalScope(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestApprovalStoreBrokerScopePassthrough verifies that the configured scope is
// attached to both allow and deny answers, and that once never sets Remember.
func TestApprovalStoreBrokerScopePassthrough(t *testing.T) {
	cases := []struct {
		scope    approval.ApprovalScope
		remember bool
	}{
		{approval.ScopeOnce, false},
		{approval.ScopeSession, true},
		{approval.ScopeWorkspace, true},
		{approval.ScopeAlways, true},
	}
	for _, tc := range cases {
		for _, approve := range []bool{true, false} {
			store := NewApprovalStore(time.Second)
			sender := &capturingApprovalSender{store: store, approve: approve}
			store.SetSender(sender)
			broker := &approvalStoreBroker{store: store, scope: tc.scope}

			resp, err := broker.Request(context.Background(), approval.ApprovalRequest{
				SessionID: "sess-1",
				ToolName:  "Bash",
				Scopes:    approval.AllScopes(),
			})
			if err != nil {
				t.Fatalf("%s approve=%v: Request: %v", tc.scope, approve, err)
			}
			if resp.Allowed() != approve {
				t.Fatalf("%s approve=%v: decision = %+v", tc.scope, approve, resp)
			}
			if resp.Scope != tc.scope || resp.Remember != tc.remember {
				t.Fatalf("%s approve=%v: scope=%q remember=%v, want %q/%v",
					tc.scope, approve, resp.Scope, resp.Remember, tc.scope, tc.remember)
			}
		}
	}
}

// TestApprovalStoreBrokerDefaultScopeIsOnce covers the zero value, which is
// what every pre-existing construction site uses.
func TestApprovalStoreBrokerDefaultScopeIsOnce(t *testing.T) {
	store := NewApprovalStore(time.Second)
	sender := &capturingApprovalSender{store: store, approve: true}
	store.SetSender(sender)

	resp, err := (&approvalStoreBroker{store: store}).Request(context.Background(), approval.ApprovalRequest{
		ToolName: "Bash",
		Scopes:   approval.AllScopes(),
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Scope != approval.ScopeOnce || resp.Remember {
		t.Fatalf("zero scope = %+v, want once without remember", resp)
	}
}

// TestApprovalStoreBrokerClampsPersistentScopeForNarrowedRequest proves a
// persistent approval_scope cannot persist a high-risk call: when the request
// only offers once/session, the answer is once.
func TestApprovalStoreBrokerClampsPersistentScopeForNarrowedRequest(t *testing.T) {
	store := NewApprovalStore(time.Second)
	sender := &capturingApprovalSender{store: store, approve: true}
	store.SetSender(sender)
	broker := &approvalStoreBroker{store: store, scope: approval.ScopeAlways}

	resp, err := broker.Request(context.Background(), approval.ApprovalRequest{
		ToolName: "Bash",
		Scopes:   []approval.ApprovalScope{approval.ScopeOnce, approval.ScopeSession},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Scope != approval.ScopeOnce || resp.Remember {
		t.Fatalf("narrowed request = %+v, want once without remember", resp)
	}
}

// TestBrokerAuthorizerHonorsRememberedDecisions is the pipeline-level check:
// a remembered allow executes without asking, a remembered deny blocks without
// asking.
func TestBrokerAuthorizerHonorsRememberedDecisions(t *testing.T) {
	call := &pipeline.Call{Name: "Bash", Args: map[string]any{"command": "git push origin main"}}
	if got := approvalRule(call); got != "git push" {
		t.Fatalf("approvalRule = %q, want %q", got, "git push")
	}

	allowStore := approval.NewScopeStore(approval.NewMemoryStore(), nil, nil)
	if err := allowStore.Session.Put(approval.PermissionRecord{
		Tool: "Bash", Pattern: "git push", Scope: approval.ScopeSession, Decision: approval.ActionAllow,
	}); err != nil {
		t.Fatalf("Put allow: %v", err)
	}
	broker := &recordingApprovalBroker{decision: approval.ActionDeny}
	auth := pipeline.BrokerAuthorizer{Broker: broker, Store: allowStore, Rule: approvalRule}
	d, err := auth.Authorize(context.Background(), call)
	if err != nil {
		t.Fatalf("allow Authorize: %v", err)
	}
	if d.Action != pipeline.ActionContinue {
		t.Fatalf("remembered allow decision = %+v", d)
	}
	if broker.Calls() != 0 {
		t.Fatalf("broker calls = %d, want 0 for a remembered allow", broker.Calls())
	}

	denyStore := approval.NewScopeStore(approval.NewMemoryStore(), nil, nil)
	if err := denyStore.Session.Put(approval.PermissionRecord{
		Tool: "Bash", Pattern: "git push", Scope: approval.ScopeSession, Decision: approval.ActionDeny,
	}); err != nil {
		t.Fatalf("Put deny: %v", err)
	}
	broker = &recordingApprovalBroker{decision: approval.ActionAllow}
	auth = pipeline.BrokerAuthorizer{Broker: broker, Store: denyStore, Rule: approvalRule}
	d, err = auth.Authorize(context.Background(), call)
	if err != nil {
		t.Fatalf("deny Authorize: %v", err)
	}
	if d.Action != pipeline.ActionBlock || !strings.Contains(d.Reason, "remembered deny") {
		t.Fatalf("remembered deny decision = %+v", d)
	}
	if broker.Calls() != 0 {
		t.Fatalf("broker calls = %d, want 0 for a remembered deny", broker.Calls())
	}
}

// TestNewApprovalScopeStoreGatesWorkspaceOnTrust covers the store construction
// rule: workspace requires a trusted home layout, always is available
// regardless.
func TestNewApprovalScopeStoreGatesWorkspaceOnTrust(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	untrusted := &homeEnv{paths: home.Paths{CWD: t.TempDir(), Root: t.TempDir()}}
	s := newApprovalScopeStore(untrusted)
	if s.Workspace != nil {
		t.Fatal("workspace store must be nil for an untrusted project")
	}
	if s.Always == nil {
		t.Fatal("always store must be available without project trust")
	}

	trusted := &homeEnv{paths: home.Paths{CWD: t.TempDir(), Root: t.TempDir()}, trusted: true}
	s = newApprovalScopeStore(trusted)
	if s.Workspace == nil {
		t.Fatal("workspace store must be available for a trusted project")
	}

	s = newApprovalScopeStore(nil)
	if s.Workspace != nil {
		t.Fatal("workspace store must be nil without a home layout")
	}
	if s.Always == nil {
		t.Fatal("always store must be available without a home layout")
	}
}

// scopedApprovalFactory builds a factory whose pipeline approval mode uses
// scope. homeCfg may set up a trusted project layout.
func scopedApprovalFactory(t *testing.T, scope string, homeCfg HomeConfig) *dinoFactory {
	t.Helper()
	cfg := getTestConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline.Enabled = true
	cfg.ToolPipeline.Authorizer = ToolPipelineAuthorizerApproval
	cfg.ToolPipeline.ApprovalScope = scope
	cfg.Home = homeCfg
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

func runBashApproval(t *testing.T, df *dinoFactory, command string) *pipeline.Result {
	t.Helper()
	tool := &countingPipeTool{name: "Bash"}
	res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:        "call-" + command,
		Name:      "Bash",
		Tool:      tool,
		SessionID: "sess-scope",
		Args:      map[string]any{"command": command},
	})
	if err != nil {
		t.Fatalf("ExecuteOne(%q): %v", command, err)
	}
	if res.IsError {
		t.Fatalf("ExecuteOne(%q) failed: %s", command, res.Text())
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool calls after %q = %d, want 1", command, tool.CallCount())
	}
	return res
}

// TestNewDinoFactoryApprovalScopeWorkspacePersists drives the whole path:
// config -> broker response -> workspace permissions.json -> remembered allow.
func TestNewDinoFactoryApprovalScopeWorkspacePersists(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "workspace")
	userHome := filepath.Join(tmp, "userhome")
	for _, dir := range []string{cwd, userHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", userHome)

	df := scopedApprovalFactory(t, "workspace", HomeConfig{
		Enabled:      true,
		Root:         filepath.Join(tmp, "home-root"),
		CWD:          cwd,
		ProjectTrust: "always",
	})
	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	runBashApproval(t, df, "git push origin main")
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1", n)
	}

	path := approval.WorkspacePermissionsPath(cwd)
	rec, ok := approval.NewFileStore(path).Lookup("Bash", "git push")
	if !ok {
		t.Fatalf("no remembered record in %s", path)
	}
	if rec.Decision != approval.ActionAllow || rec.Scope != approval.ScopeWorkspace {
		t.Fatalf("remembered record = %+v, want workspace allow", rec)
	}

	// Same command pattern under a different argument list: no second prompt.
	runBashApproval(t, df, "git push --force")
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1 (remembered)", n)
	}
}

// TestNewDinoFactoryApprovalScopeAlwaysPersistsToUserHome covers the user-wide
// store location.
func TestNewDinoFactoryApprovalScopeAlwaysPersistsToUserHome(t *testing.T) {
	tmp := t.TempDir()
	userHome := filepath.Join(tmp, "userhome")
	if err := os.MkdirAll(userHome, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", userHome)

	df := scopedApprovalFactory(t, "always", HomeConfig{})
	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	runBashApproval(t, df, "git status")

	path := approval.UserPermissionsPath(userHome)
	rec, ok := approval.NewFileStore(path).Lookup("Bash", "git status")
	if !ok {
		t.Fatalf("no remembered record in %s", path)
	}
	if rec.Decision != approval.ActionAllow || rec.Scope != approval.ScopeAlways {
		t.Fatalf("remembered record = %+v, want always allow", rec)
	}
}

// TestNewDinoFactoryApprovalScopeUntrustedWorkspaceIsNotPersisted proves the
// trust gate: an untrusted project cannot write permissions.json, while the
// call itself still runs on an allow.
func TestNewDinoFactoryApprovalScopeUntrustedWorkspaceIsNotPersisted(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", filepath.Join(tmp, "userhome"))

	df := scopedApprovalFactory(t, "workspace", HomeConfig{
		Enabled:      true,
		Root:         filepath.Join(tmp, "home-root"),
		CWD:          cwd,
		ProjectTrust: "never",
	})
	sender := &capturingApprovalSender{store: df.approvalStore, approve: true}
	df.approvalStore.SetSender(sender)

	runBashApproval(t, df, "git push")
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1", n)
	}
	if _, err := os.Stat(approval.WorkspacePermissionsPath(cwd)); !os.IsNotExist(err) {
		t.Fatalf("untrusted project wrote permissions.json: %v", err)
	}

	// No home layout at all: the workspace store stays empty too, and the call
	// still runs (a Remember failure must never turn into a denial).
	dfNoHome := scopedApprovalFactory(t, "workspace", HomeConfig{})
	sender = &capturingApprovalSender{store: dfNoHome.approvalStore, approve: true}
	dfNoHome.approvalStore.SetSender(sender)
	runBashApproval(t, dfNoHome, "git push")
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1", n)
	}
}

// TestNewDinoFactoryRememberedDenyBlocksWithoutAsking verifies the deny side of
// scope persistence end to end.
func TestNewDinoFactoryRememberedDenyBlocksWithoutAsking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	df := scopedApprovalFactory(t, "session", HomeConfig{})
	sender := &capturingApprovalSender{store: df.approvalStore, approve: false}
	df.approvalStore.SetSender(sender)

	tool := &countingPipeTool{name: "Bash"}
	res, err := df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:        "c1",
		Name:      "Bash",
		Tool:      tool,
		SessionID: "sess-deny",
		Args:      map[string]any{"command": "rm -rf build"},
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.IsError {
		t.Fatalf("denied call must fail, got %q", res.Text())
	}
	if tool.CallCount() != 0 {
		t.Fatalf("denied tool executed %d times", tool.CallCount())
	}

	tool2 := &countingPipeTool{name: "Bash"}
	res, err = df.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:        "c2",
		Name:      "Bash",
		Tool:      tool2,
		SessionID: "sess-deny",
		Args:      map[string]any{"command": "rm -rf build"},
	})
	if err != nil {
		t.Fatalf("second ExecuteOne: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "remembered deny") {
		t.Fatalf("second call = %q (isError=%v), want remembered deny", res.Text(), res.IsError)
	}
	if tool2.CallCount() != 0 {
		t.Fatalf("remembered deny still executed the tool")
	}
	if n, _, _, _, _ := sender.Snapshot(); n != 1 {
		t.Fatalf("approval requests = %d, want 1 (remembered deny)", n)
	}
}

// TestApprovalRulePatterns documents the remembered-key shape per tool.
func TestApprovalRulePatterns(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"Bash", map[string]any{"command": "  git   push   origin main "}, "git push"},
		{"Bash", map[string]any{"command": "ls"}, "ls"},
		{"Bash", map[string]any{}, "*"},
		{"execute_command", map[string]any{"command": "git push"}, "git push"},
		{"Write", map[string]any{"path": "./a/../b.txt"}, "b.txt"},
		{"Edit", map[string]any{"file_path": "/tmp/x.go"}, "/tmp/x.go"},
		{"Read", map[string]any{"path": "notes.md"}, "notes.md"},
		{"question", map[string]any{"question": "go?"}, "*"},
	}
	for _, tc := range cases {
		got := approvalRule(&pipeline.Call{Name: tc.name, Args: tc.args})
		if got != tc.want {
			t.Fatalf("approvalRule(%s, %v) = %q, want %q", tc.name, tc.args, got, tc.want)
		}
	}
	if got := approvalRule(nil); got != "*" {
		t.Fatalf("approvalRule(nil) = %q, want *", got)
	}
}
