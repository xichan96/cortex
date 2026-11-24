package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

func TestAllowAndDenyBrokers(t *testing.T) {
	resp, err := AllowBroker{}.Request(context.Background(), ApprovalRequest{})
	if err != nil || !resp.Allowed() || resp.Scope != ScopeOnce {
		t.Fatalf("allow broker: %+v err=%v", resp, err)
	}

	resp, err = (DenyBroker{Reason: "nope"}).Request(context.Background(), ApprovalRequest{})
	if err != nil {
		t.Fatalf("deny broker returned error: %v", err)
	}
	if resp.Allowed() || resp.Reason != "nope" {
		t.Fatalf("deny broker: %+v", resp)
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemoryStore()
	rec := PermissionRecord{Tool: "Bash", Pattern: "git push:*", Scope: ScopeSession, Decision: ActionAllow}
	if err := s.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := s.Lookup("Bash", "git push:*")
	if !ok || got.Decision != ActionAllow || got.CreatedAt.IsZero() {
		t.Fatalf("lookup: %+v ok=%v", got, ok)
	}
	if _, ok := s.Lookup("Bash", "rm:*"); ok {
		t.Fatal("unexpected match")
	}
	list := s.List()
	if len(list) != 1 || list[0].Key() != "Bash:git push:*" {
		t.Fatalf("list = %+v", list)
	}
	if err := s.Remove("Bash", "git push:*"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := s.Lookup("Bash", "git push:*"); ok {
		t.Fatal("record not removed")
	}
}

func TestMemoryStoreRejectsRecordWithoutTool(t *testing.T) {
	if err := NewMemoryStore().Put(PermissionRecord{Pattern: "*"}); err == nil {
		t.Fatal("expected error for record without tool")
	}
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".cortex", "permissions.json")
	s := NewFileStore(path)
	if err := s.Put(PermissionRecord{Tool: "write", Pattern: "a.txt", Scope: ScopeWorkspace, Decision: ActionAllow}); err != nil {
		t.Fatalf("put: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	reloaded := NewFileStore(path)
	rec, ok := reloaded.Lookup("write", "a.txt")
	if !ok || rec.Decision != ActionAllow || rec.Scope != ScopeWorkspace {
		t.Fatalf("reloaded lookup: %+v ok=%v", rec, ok)
	}

	if err := reloaded.Remove("write", "a.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := NewFileStore(path).Lookup("write", "a.txt"); ok {
		t.Fatal("removal not persisted")
	}
}

func TestFileStoreMissingFileIsEmpty(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "absent.json"))
	if _, ok := s.Lookup("x", "*"); ok {
		t.Fatal("unexpected record")
	}
	if len(s.List()) != 0 {
		t.Fatal("expected empty list")
	}
}

func TestFileStoreAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")
	s := NewFileStore(path)
	if err := s.Put(PermissionRecord{Tool: "t", Decision: ActionAllow}); err != nil {
		t.Fatalf("put: %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".permissions-*.tmp"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("temp files left behind: %v", temps)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"tool": "t"`) {
		t.Fatalf("unexpected file contents: %s", data)
	}
}

func TestFileStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFileStore(path)
	if _, ok := s.Lookup("t", "*"); ok {
		t.Fatal("corrupt store should not yield records")
	}
	if err := s.Put(PermissionRecord{Tool: "t", Decision: ActionAllow}); err == nil {
		t.Fatal("expected put to fail on corrupt store")
	}
}

func TestScopeStoreRememberRouting(t *testing.T) {
	session, workspace, always := NewMemoryStore(), NewMemoryStore(), NewMemoryStore()
	s := NewScopeStore(session, workspace, always)
	req := ApprovalRequest{ToolName: "Bash", Rule: "git push:*"}

	cases := []struct {
		name  string
		resp  ApprovalResponse
		store *MemoryStore
	}{
		{"session", ApprovalResponse{Decision: ActionAllow, Scope: ScopeSession, Remember: true}, session},
		{"workspace", ApprovalResponse{Decision: ActionAllow, Scope: ScopeWorkspace, Remember: true}, workspace},
		{"always", ApprovalResponse{Decision: ActionAllow, Scope: ScopeAlways, Remember: true}, always},
	}
	for _, tc := range cases {
		if err := s.Remember(req, tc.resp); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, ok := tc.store.Lookup("Bash", "git push:*"); !ok {
			t.Fatalf("%s: record not routed", tc.name)
		}
	}

	// once and Remember=false are no-ops even when stores exist.
	if err := s.Remember(req, ApprovalResponse{Decision: ActionAllow, Scope: ScopeOnce, Remember: true}); err != nil {
		t.Fatalf("once: %v", err)
	}
	if err := s.Remember(req, ApprovalResponse{Decision: ActionAllow, Scope: ScopeSession}); err != nil {
		t.Fatalf("no remember: %v", err)
	}
	if len(session.List())+len(workspace.List())+len(always.List()) != 3 {
		t.Fatalf("unexpected extra records: %d/%d/%d",
			len(session.List()), len(workspace.List()), len(always.List()))
	}
}

func TestScopeStoreUnavailable(t *testing.T) {
	s := NewScopeStore(nil, nil, nil)
	err := s.Remember(ApprovalRequest{ToolName: "t"}, ApprovalResponse{
		Decision: ActionAllow, Scope: ScopeWorkspace, Remember: true,
	})
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("err = %v, want ErrStoreUnavailable", err)
	}
}

func TestScopeStoreLookupPrefersSession(t *testing.T) {
	session := NewMemoryStore()
	workspace := NewMemoryStore()
	_ = session.Put(PermissionRecord{Tool: "t", Decision: ActionAllow, Scope: ScopeSession})
	_ = workspace.Put(PermissionRecord{Tool: "t", Decision: ActionDeny, Scope: ScopeWorkspace})
	s := NewScopeStore(session, workspace, nil)
	rec, ok := s.Lookup("t", "")
	if !ok || rec.Decision != ActionAllow {
		t.Fatalf("lookup = %+v ok=%v, want session allow", rec, ok)
	}
}

func TestScopeStoreNilReceiverLookup(t *testing.T) {
	var s *ScopeStore
	if _, ok := s.Lookup("t", "*"); ok {
		t.Fatal("nil ScopeStore should not match")
	}
}

func TestScopesForRisk(t *testing.T) {
	all := ScopesForRisk(RiskLow, nil)
	if len(all) != 4 {
		t.Fatalf("low risk scopes = %v", all)
	}
	high := ScopesForRisk(RiskHigh, nil)
	if len(high) != 2 || high[0] != ScopeOnce || high[1] != ScopeSession {
		t.Fatalf("high risk scopes = %v", high)
	}
	onlyWorkspace := ScopesForRisk(RiskHigh, []ApprovalScope{ScopeWorkspace})
	if len(onlyWorkspace) != 2 {
		t.Fatalf("high risk with no narrow scope should fall back: %v", onlyWorkspace)
	}
}

func TestScopeHelpers(t *testing.T) {
	if !ScopeWorkspace.IsPersistent() || ScopeSession.IsPersistent() {
		t.Fatal("IsPersistent wrong")
	}
	if ScopeOnce.Rememberable() || !ScopeSession.Rememberable() {
		t.Fatal("Rememberable wrong")
	}
	if !ScopeAlways.IsValid() || ApprovalScope("bogus").IsValid() {
		t.Fatal("IsValid wrong")
	}
	if got := WorkspacePermissionsPath("/w"); got != filepath.Join("/w", ".cortex", "permissions.json") {
		t.Fatalf("workspace path = %s", got)
	}
	if got := UserPermissionsPath("/home/u"); got != filepath.Join("/home/u", ".cortex", "permissions.json") {
		t.Fatalf("user path = %s", got)
	}
}

func TestRiskIsReexportedTypesType(t *testing.T) {
	var r RiskLevel = types.RiskHigh
	if !r.IsHigh() {
		t.Fatal("risk alias mismatch")
	}
}

func TestFileStoreListSorted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	s := NewFileStore(path)
	for _, tool := range []string{"c", "a", "b"} {
		if err := s.Put(PermissionRecord{Tool: tool, Decision: ActionAllow, CreatedAt: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	list := s.List()
	for i := 1; i < len(list); i++ {
		if list[i-1].Key() > list[i].Key() {
			t.Fatalf("not sorted: %+v", list)
		}
	}
}
