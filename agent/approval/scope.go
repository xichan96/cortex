// Package approval holds the transport-neutral approval model: the Broker
// interface, approval scopes, and the remembered-permission stores.
//
// It deliberately has no dependency on dino or on any transport. Hosts adapt
// their own channel/UI approval mechanism into a Broker; the tool pipeline
// consumes that Broker in its authorize stage.
package approval

import (
	"path/filepath"

	"github.com/xichan96/cortex/agent/types"
)

// RiskLevel is re-exported so callers of the approval API need only this
// package. It is the same type used by tool metadata.
type RiskLevel = types.RiskLevel

const (
	RiskLow    = types.RiskLow
	RiskMedium = types.RiskMedium
	RiskHigh   = types.RiskHigh
)

// ApprovalScope controls how long an approval decision is remembered (§5.5).
type ApprovalScope string

const (
	// ScopeOnce applies only to the current call; nothing is persisted.
	ScopeOnce ApprovalScope = "once"
	// ScopeSession applies for the lifetime of the current session (in memory).
	ScopeSession ApprovalScope = "session"
	// ScopeWorkspace is remembered for the project in
	// <workspace>/.cortex/permissions.json.
	ScopeWorkspace ApprovalScope = "workspace"
	// ScopeAlways is remembered for the user in ~/.cortex/permissions.json.
	ScopeAlways ApprovalScope = "always"
)

// AllScopes lists scopes from narrowest to widest. It is the default scope set
// offered to a broker for a low-risk request.
func AllScopes() []ApprovalScope {
	return []ApprovalScope{ScopeOnce, ScopeSession, ScopeWorkspace, ScopeAlways}
}

// IsValid reports whether s is a declared scope. The empty value is treated as
// ScopeOnce (zero value, nothing remembered) for leniency.
func (s ApprovalScope) IsValid() bool {
	switch s {
	case "", ScopeOnce, ScopeSession, ScopeWorkspace, ScopeAlways:
		return true
	default:
		return false
	}
}

// IsPersistent reports whether the scope writes permissions.json on disk.
func (s ApprovalScope) IsPersistent() bool {
	return s == ScopeWorkspace || s == ScopeAlways
}

// Rememberable reports whether a decision with this scope should be stored.
// Once never is. The empty value is once.
func (s ApprovalScope) Rememberable() bool {
	return s == ScopeSession || s == ScopeWorkspace || s == ScopeAlways
}

// ScopesForRisk narrows the offered scopes for high-risk calls: high risk may
// only be remembered for once/session (§5.6). Low/medium/unknown risk keeps the
// provided scopes (or AllScopes when empty).
func ScopesForRisk(risk RiskLevel, scopes []ApprovalScope) []ApprovalScope {
	if len(scopes) == 0 {
		scopes = AllScopes()
	}
	if !risk.IsHigh() {
		return append([]ApprovalScope(nil), scopes...)
	}
	narrowed := make([]ApprovalScope, 0, 2)
	for _, s := range scopes {
		if s == ScopeOnce || s == ScopeSession {
			narrowed = append(narrowed, s)
		}
	}
	if len(narrowed) == 0 {
		narrowed = []ApprovalScope{ScopeOnce, ScopeSession}
	}
	return narrowed
}

// WorkspacePermissionsPath is the project-level permissions file.
func WorkspacePermissionsPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".cortex", "permissions.json")
}

// UserPermissionsPath is the user-level permissions file.
func UserPermissionsPath(homeDir string) string {
	return filepath.Join(homeDir, ".cortex", "permissions.json")
}
