package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newGrepFixture writes a temp workspace with a known file and returns a
// GrepTool bound to it plus the workspace path.
func newGrepFixture(t *testing.T) (string, *GrepTool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("alpha\nbeta\nalpha gamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, NewGrepTool(dir).(*GrepTool)
}

func TestGrep_HasMatches(t *testing.T) {
	dir, tool := newGrepFixture(t)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "alpha"})
	if err != nil {
		t.Fatalf("matched grep must not error: %v", err)
	}
	out := res.(string)
	if !strings.Contains(out, "sample.txt") || !strings.Contains(out, "alpha") {
		t.Fatalf("expected match output, got %q", out)
	}
	_ = dir
}

func TestGrep_NoMatchReturnsEmptyNotError(t *testing.T) {
	_, tool := newGrepFixture(t)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "zzz-no-such-pattern"})
	if err != nil {
		t.Fatalf("no-match grep must NOT be an error (exit 1 treated as empty result): %v", err)
	}
	if res != "" {
		t.Fatalf("no-match must return empty string, got %q", res)
	}
}

func TestGrep_BadPathReturnsError(t *testing.T) {
	_, tool := newGrepFixture(t)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "x", "path": "/does/not/exist"}); err == nil {
		t.Fatal("grep on a missing path must error (exit 2)")
	}
}
