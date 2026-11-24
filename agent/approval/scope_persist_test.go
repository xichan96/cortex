package approval

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScopeStorePersistsEachScopeToItsStore pins the persistence contract of
// §5.5: session decisions stay in memory, workspace decisions land in the
// project permissions file, and always decisions land in the user one.
func TestScopeStorePersistsEachScopeToItsStore(t *testing.T) {
	dir := t.TempDir()
	workspacePath := WorkspacePermissionsPath(filepath.Join(dir, "ws"))
	userPath := UserPermissionsPath(filepath.Join(dir, "home"))

	session := NewMemoryStore()
	workspace := NewFileStore(workspacePath)
	always := NewFileStore(userPath)
	store := NewScopeStore(session, workspace, always)

	cases := []struct {
		scope ApprovalScope
		rule  string
	}{
		{ScopeSession, "session-rule"},
		{ScopeWorkspace, "workspace-rule"},
		{ScopeAlways, "always-rule"},
	}
	for _, tc := range cases {
		req := ApprovalRequest{ToolName: "Bash", Rule: tc.rule}
		resp := ApprovalResponse{Decision: ActionAllow, Scope: tc.scope, Remember: true}
		if err := store.Remember(req, resp); err != nil {
			t.Fatalf("Remember(%s): %v", tc.scope, err)
		}
	}

	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("workspace permissions file missing: %v", err)
	}
	if _, err := os.Stat(userPath); err != nil {
		t.Fatalf("user permissions file missing: %v", err)
	}

	for _, tc := range cases {
		rec, ok := store.Lookup("Bash", tc.rule)
		if !ok {
			t.Fatalf("Lookup(%s) missed", tc.scope)
		}
		if rec.Scope != tc.scope || rec.Decision != ActionAllow {
			t.Fatalf("Lookup(%s) = %+v", tc.scope, rec)
		}
	}

	if got := len(session.List()); got != 1 {
		t.Fatalf("session store has %d records, want 1", got)
	}
	if got := len(workspace.List()); got != 1 {
		t.Fatalf("workspace store has %d records, want 1", got)
	}
	if got := len(always.List()); got != 1 {
		t.Fatalf("always store has %d records, want 1", got)
	}
}

// TestScopeStoreRememberOnceWritesNothing proves once (and a not-remembered
// response) never touches disk.
func TestScopeStoreRememberOnceWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := UserPermissionsPath(filepath.Join(dir, "home"))
	store := NewScopeStore(NewMemoryStore(), nil, NewFileStore(path))

	req := ApprovalRequest{ToolName: "Bash", Rule: "git push"}
	if err := store.Remember(req, ApprovalResponse{Decision: ActionAllow, Scope: ScopeOnce, Remember: true}); err != nil {
		t.Fatalf("Remember(once): %v", err)
	}
	if err := store.Remember(req, ApprovalResponse{Decision: ActionAllow, Scope: ScopeAlways, Remember: false}); err != nil {
		t.Fatalf("Remember(not remembered): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("permissions file written for a non-remembered decision: %v", err)
	}
}

// TestFileStoreRoundTrip reloads the same file through a fresh store, which is
// what a new process does.
func TestFileStoreRoundTrip(t *testing.T) {
	path := UserPermissionsPath(filepath.Join(t.TempDir(), "home"))

	first := NewFileStore(path)
	if err := first.Put(PermissionRecord{Tool: "Bash", Pattern: "git push", Scope: ScopeAlways, Decision: ActionAllow}); err != nil {
		t.Fatalf("Put allow: %v", err)
	}
	if err := first.Put(PermissionRecord{Tool: "Write", Pattern: "secret.txt", Scope: ScopeWorkspace, Decision: ActionDeny}); err != nil {
		t.Fatalf("Put deny: %v", err)
	}

	second := NewFileStore(path)
	rec, ok := second.Lookup("Bash", "git push")
	if !ok || rec.Decision != ActionAllow || rec.Scope != ScopeAlways {
		t.Fatalf("reloaded Bash record = %+v ok=%v", rec, ok)
	}
	rec, ok = second.Lookup("Write", "secret.txt")
	if !ok || rec.Decision != ActionDeny {
		t.Fatalf("reloaded Write record = %+v ok=%v", rec, ok)
	}
	if got := len(second.List()); got != 2 {
		t.Fatalf("reloaded %d records, want 2", got)
	}

	if err := second.Remove("Bash", "git push"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	third := NewFileStore(path)
	if _, ok := third.Lookup("Bash", "git push"); ok {
		t.Fatal("removed record was reloaded")
	}
	if got := len(third.List()); got != 1 {
		t.Fatalf("after remove %d records, want 1", got)
	}
}

// TestScopeStoreLookupRemembersAllowAndDeny covers both decision kinds and the
// session > workspace > always precedence.
func TestScopeStoreLookupRemembersAllowAndDeny(t *testing.T) {
	session := NewMemoryStore()
	workspace := NewMemoryStore()
	always := NewMemoryStore()
	store := NewScopeStore(session, workspace, always)

	if err := workspace.Put(PermissionRecord{Tool: "Bash", Pattern: "ls", Scope: ScopeWorkspace, Decision: ActionAllow}); err != nil {
		t.Fatalf("Put allow: %v", err)
	}
	if err := always.Put(PermissionRecord{Tool: "Bash", Pattern: "rm -rf", Scope: ScopeAlways, Decision: ActionDeny}); err != nil {
		t.Fatalf("Put deny: %v", err)
	}

	if rec, ok := store.Lookup("Bash", "ls"); !ok || rec.Decision != ActionAllow {
		t.Fatalf("remembered allow = %+v ok=%v", rec, ok)
	}
	if rec, ok := store.Lookup("Bash", "rm -rf"); !ok || rec.Decision != ActionDeny {
		t.Fatalf("remembered deny = %+v ok=%v", rec, ok)
	}

	// A session deny shadows a matching workspace record.
	if err := session.Put(PermissionRecord{Tool: "Bash", Pattern: "ls", Scope: ScopeSession, Decision: ActionDeny}); err != nil {
		t.Fatalf("Put session deny: %v", err)
	}
	if rec, ok := store.Lookup("Bash", "ls"); !ok || rec.Decision != ActionDeny {
		t.Fatalf("session precedence = %+v ok=%v", rec, ok)
	}

	if _, ok := store.Lookup("Bash", "unknown"); ok {
		t.Fatal("unknown rule must not resolve")
	}
}

// TestParseScope covers the config-facing normalization: only exact persistent
// names are accepted, everything else degrades to once.
func TestParseScope(t *testing.T) {
	cases := map[string]ApprovalScope{
		"":          ScopeOnce,
		"once":      ScopeOnce,
		"session":   ScopeSession,
		"workspace": ScopeWorkspace,
		"always":    ScopeAlways,
		" always ":  ScopeAlways,
		"ALWAYS":    ScopeOnce,
		"bogus":     ScopeOnce,
	}
	for raw, want := range cases {
		if got := ParseScope(raw); got != want {
			t.Fatalf("ParseScope(%q) = %q, want %q", raw, got, want)
		}
	}
}
