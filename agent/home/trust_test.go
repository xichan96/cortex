package home

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTrustOrder(t *testing.T) {
	ctx := context.Background()
	store := NewFileTrustStore(filepath.Join(t.TempDir(), "trust.json"))
	if err := store.Save(ctx, "/repo", TrustAllowed); err != nil {
		t.Fatal(err)
	}
	req := TrustRequest{CWD: "/repo/sub", Mode: TrustModeInteractive}

	// 1. CLI override beats everything, including the handler.
	decision, err := ResolveTrust(ctx, req, store, TrustOptions{
		CLIOverride: TrustDenied,
		Handler: func(context.Context, TrustRequest) (TrustDecision, bool, error) {
			return TrustAllowed, true, nil
		},
		DefaultPolicy: TrustPolicyAlways,
	})
	if err != nil || decision != TrustDenied {
		t.Fatalf("CLI override: decision=%v err=%v", decision, err)
	}

	// 2. Handler beats the stored ancestor decision.
	decision, err = ResolveTrust(ctx, req, store, TrustOptions{
		Handler: func(context.Context, TrustRequest) (TrustDecision, bool, error) {
			return TrustDenied, true, nil
		},
		DefaultPolicy: TrustPolicyAlways,
	})
	if err != nil || decision != TrustDenied {
		t.Fatalf("handler: decision=%v err=%v", decision, err)
	}

	// A handler without an opinion falls through.
	decision, err = ResolveTrust(ctx, req, store, TrustOptions{
		Handler: func(context.Context, TrustRequest) (TrustDecision, bool, error) {
			return TrustUnknown, false, nil
		},
	})
	if err != nil || decision != TrustAllowed {
		t.Fatalf("fallthrough to store: decision=%v err=%v", decision, err)
	}

	// 3. The nearest stored ancestor applies when no override exists.
	decision, err = ResolveTrust(ctx, TrustRequest{CWD: "/repo/other"}, store, TrustOptions{})
	if err != nil || decision != TrustAllowed {
		t.Fatalf("store ancestor: decision=%v err=%v", decision, err)
	}

	// 4. Nobody has an opinion: interactive ask returns unknown.
	decision, err = ResolveTrust(ctx, TrustRequest{CWD: "/elsewhere", Mode: TrustModeInteractive}, store, TrustOptions{})
	if err != nil || decision != TrustUnknown {
		t.Fatalf("interactive ask: decision=%v err=%v", decision, err)
	}
	if decision, _ = ResolveTrust(ctx, TrustRequest{CWD: "/elsewhere", Mode: TrustModeNonInteractive}, store, TrustOptions{}); decision != TrustDenied {
		t.Fatalf("non-interactive ask should deny, got %v", decision)
	}
}

func TestResolveTrustPolicies(t *testing.T) {
	ctx := context.Background()
	req := TrustRequest{CWD: "/repo", Mode: TrustModeInteractive}
	cases := []struct {
		policy TrustPolicy
		mode   TrustMode
		want   TrustDecision
	}{
		{TrustPolicyAlways, TrustModeInteractive, TrustAllowed},
		{TrustPolicyAlways, TrustModeNonInteractive, TrustAllowed},
		{TrustPolicyNever, TrustModeInteractive, TrustDenied},
		{TrustPolicyNever, TrustModeNonInteractive, TrustDenied},
		{TrustPolicyAsk, TrustModeInteractive, TrustUnknown},
		{TrustPolicyAsk, TrustModeNonInteractive, TrustDenied},
	}
	for _, tc := range cases {
		req.Mode = tc.mode
		got, err := ResolveTrust(ctx, req, nil, TrustOptions{DefaultPolicy: tc.policy})
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.policy, tc.mode, err)
		}
		if got != tc.want {
			t.Errorf("%s/%s = %v, want %v", tc.policy, tc.mode, got, tc.want)
		}
	}
}

func TestFileTrustStoreNearestAncestor(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	parent := filepath.Join(root, "a")
	child := filepath.Join(parent, "b")
	sibling := filepath.Join(parent, "c")
	for _, dir := range []string{child, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	store := NewFileTrustStore(filepath.Join(t.TempDir(), "trust.json"))
	if err := store.Save(ctx, parent, TrustAllowed); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, child, TrustDenied); err != nil {
		t.Fatal(err)
	}

	if d, found, err := store.Lookup(ctx, child); err != nil || !found || d != TrustDenied {
		t.Fatalf("child lookup = %v found=%v err=%v", d, found, err)
	}
	if d, found, err := store.Lookup(ctx, sibling); err != nil || !found || d != TrustAllowed {
		t.Fatalf("sibling lookup = %v found=%v err=%v", d, found, err)
	}
	if d, found, err := store.Lookup(ctx, filepath.Join(child, "deeper")); err != nil || !found || d != TrustDenied {
		t.Fatalf("deep lookup = %v found=%v err=%v", d, found, err)
	}

	// Removing the child decision falls back to the parent.
	if err := store.Save(ctx, child, TrustUnknown); err != nil {
		t.Fatal(err)
	}
	if d, found, _ := store.Lookup(ctx, child); !found || d != TrustAllowed {
		t.Fatalf("after removal = %v found=%v", d, found)
	}

	// Unknown paths have no decision.
	if _, found, _ := store.Lookup(ctx, t.TempDir()); found {
		t.Fatal("unrelated path unexpectedly found")
	}
}

func TestFileTrustStoreWritesJSON(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "trust.json")
	store := NewFileTrustStore(path)

	root := t.TempDir()
	if err := store.Save(ctx, root, TrustAllowed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]bool
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("trust.json is not JSON: %v", err)
	}
	if !entries[CanonicalPath(root)] {
		t.Fatalf("entries = %v, want canonical key %q", entries, CanonicalPath(root))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("trust.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestFileTrustStoreCorruptFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileTrustStore(path)
	if _, _, err := store.Lookup(ctx, "/repo"); err == nil {
		t.Fatal("corrupt trust store should return an error")
	}
}

func TestCanonicalPathResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got := CanonicalPath(link)
	resolvedReal, _ := filepath.EvalSymlinks(realDir)
	if got != resolvedReal {
		t.Fatalf("CanonicalPath(%q) = %q, want %q", link, got, resolvedReal)
	}

	// Non-existent children still resolve through the existing parent.
	child := filepath.Join(link, "new-dir")
	if got := CanonicalPath(child); got != filepath.Join(resolvedReal, "new-dir") {
		t.Fatalf("CanonicalPath(%q) = %q", child, got)
	}
}

func TestParseTrustPolicy(t *testing.T) {
	cases := map[string]TrustPolicy{
		"always":  TrustPolicyAlways,
		" NEVER ": TrustPolicyNever,
		"ask":     TrustPolicyAsk,
		"bogus":   TrustPolicyAsk,
		"":        TrustPolicyAsk,
	}
	for in, want := range cases {
		if got := ParseTrustPolicy(in); got != want {
			t.Errorf("ParseTrustPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}
