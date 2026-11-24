package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/dino/permission"
)

// approvalStoreBroker adapts dino's ApprovalStore to the transport-neutral
// approval.Broker interface used by the pipeline's authorize stage (design
// §5.2: ChannelBroker / UI bridge). Session routing goes through
// req.SessionID, which the engine stamps per call.
//
// scope is the configured approval_scope: every answer (allow and deny) is
// tagged with it, and Remember is set for the scopes that should persist. The
// zero value means "once". The broker never remembers anything itself; the
// pipeline's BrokerAuthorizer (allow) and rememberingBroker (deny) persist the
// decision.
type approvalStoreBroker struct {
	store *ApprovalStore
	scope approval.ApprovalScope
}

// effectiveScope normalizes the configured scope and clamps it to the scopes
// the request actually offers. Request.Scopes is narrowed for high-risk calls
// (§5.6: only once/session), so a persistent configuration cannot leak a
// high-risk approval to disk.
func (b *approvalStoreBroker) effectiveScope(req approval.ApprovalRequest) approval.ApprovalScope {
	scope := b.scope
	if scope == "" {
		scope = approval.ScopeOnce
	}
	if len(req.Scopes) == 0 {
		return scope
	}
	for _, offered := range req.Scopes {
		if offered == scope {
			return scope
		}
	}
	return approval.ScopeOnce
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
	scope := b.effectiveScope(req)
	if approved {
		return approval.ApprovalResponse{
			Decision: approval.ActionAllow,
			Scope:    scope,
			Remember: scope.Rememberable(),
		}, nil
	}
	return approval.ApprovalResponse{
		Decision: approval.ActionDeny,
		Scope:    scope,
		Remember: scope.Rememberable(),
		Reason:   "approval denied",
	}, nil
}

// rememberingBroker persists denied decisions. BrokerAuthorizer already stores
// approved rememberable responses; a denied response must be remembered too so
// a remembered deny blocks subsequent calls without asking again. Persistence
// failures are ignored: the decision itself is already final.
type rememberingBroker struct {
	inner approval.Broker
	store *approval.ScopeStore
}

// Request implements approval.Broker.
func (b *rememberingBroker) Request(ctx context.Context, req approval.ApprovalRequest) (approval.ApprovalResponse, error) {
	if b == nil || b.inner == nil {
		return approval.ApprovalResponse{}, approval.ErrApprovalUnavailable
	}
	resp, err := b.inner.Request(ctx, req)
	if err != nil {
		return resp, err
	}
	if !resp.Allowed() && resp.Remember && b.store != nil {
		_ = b.store.Remember(req, resp)
	}
	return resp, nil
}

// approvalRule derives the remembered-permission pattern for a call. The store
// key is (tool, pattern), so the pattern covers only the argument that makes
// the decision reusable: the first two words of a shell command ("git push"),
// or the cleaned path of a file tool. Everything else is "*" (the whole tool).
func approvalRule(c *pipeline.Call) string {
	if c == nil || len(c.Args) == 0 {
		return "*"
	}
	switch c.Name {
	case "Bash", "execute_command", "command":
		if cmd := stringArg(c.Args, "command"); cmd != "" {
			return commandPattern(cmd)
		}
	case "write_file", "edit_file", "read_file", "file":
		if p := stringArg(c.Args, "path", "file_path", "filepath"); p != "" {
			return filepath.Clean(p)
		}
	}
	return "*"
}

func stringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// commandPattern keeps at most the first two whitespace-separated words so
// "git push origin main" and "git push --force" share one remembered decision.
func commandPattern(cmd string) string {
	fields := strings.Fields(cmd)
	switch len(fields) {
	case 0:
		return "*"
	case 1:
		return fields[0]
	default:
		return fields[0] + " " + fields[1]
	}
}

// newApprovalScopeStore builds the remembered-permission stores for pipeline
// approval mode (§5.5):
//
//	session   → in-memory, per factory process
//	workspace → <cwd>/.cortex/permissions.json, only when the project is trusted
//	always    → ~/.cortex/permissions.json
//
// os.UserHomeDir is authoritative for "always"; when it fails and a home layout
// is configured, the parent of the home root is used instead. A nil store means
// that scope cannot be remembered (Remember returns ErrStoreUnavailable, which
// callers ignore).
func newApprovalScopeStore(home *homeEnv) *approval.ScopeStore {
	session := approval.NewMemoryStore()

	var workspace approval.Store
	if home != nil && home.trusted && home.paths.CWD != "" {
		workspace = approval.NewFileStore(approval.WorkspacePermissionsPath(home.paths.CWD))
	}

	var always approval.Store
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		always = approval.NewFileStore(approval.UserPermissionsPath(userHome))
	} else if home != nil && home.paths.Root != "" {
		always = approval.NewFileStore(approval.UserPermissionsPath(filepath.Dir(home.paths.Root)))
	}

	return approval.NewScopeStore(session, workspace, always)
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
	// store holds remembered decisions (session/workspace/always). Nil means
	// nothing is remembered and no lookup happens.
	store *approval.ScopeStore
	// scopes are offered to the broker; nil means approval.AllScopes.
	scopes []approval.ApprovalScope
	// rule names the remembered pattern. Nil means the whole tool ("*").
	rule func(c *pipeline.Call) string
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
		broker := a.broker
		if a.store != nil {
			// BrokerAuthorizer persists allow decisions; remember deny in the
			// adapter so a remembered deny is honored on later calls too.
			broker = &rememberingBroker{inner: broker, store: a.store}
		}
		return pipeline.BrokerAuthorizer{
			Broker:  broker,
			Scopes:  a.scopes,
			Timeout: a.timeout,
			Rule:    a.rule,
			Store:   a.store,
		}.Authorize(ctx, c)
	default:
		return pipeline.Block("denied by permission: " + name), nil
	}
}
