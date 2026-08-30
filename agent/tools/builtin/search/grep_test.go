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

// TestExcludeDirArgs 验证排除参数包含常见噪音目录。
func TestExcludeDirArgs(t *testing.T) {
	args := excludeDirArgs()
	joined := strings.Join(args, " ")
	for _, dir := range []string{".git", "node_modules", "vendor"} {
		if !strings.Contains(joined, "--exclude-dir "+dir) {
			t.Errorf("missing exclude-dir %s in %q", dir, joined)
		}
	}
}

// TestGitignorePath 验证 workspace 根 .gitignore 被检测；无则返回空。
func TestGitignorePath(t *testing.T) {
	dir := t.TempDir()
	if got := gitignorePath(dir); got != "" {
		t.Errorf("no .gitignore expected, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitignorePath(dir); got == "" {
		t.Error("expected .gitignore path")
	}
	// workspace "*" (unbound) → no ignore file.
	if got := gitignorePath("*"); got != "" {
		t.Errorf("unbound workspace should not resolve .gitignore, got %q", got)
	}
}

// TestGrepIgnoresVendorDir 端到端：vendor 目录内容不应出现在 grep 结果里。
func TestGrepIgnoresVendorDir(t *testing.T) {
	dir, tool := newGrepFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep", "lib.go"), []byte("alpha secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "alpha",
		"path":    ".",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(res.(string), "vendor") {
		t.Errorf("grep result must exclude vendor dir, got: %q", res.(string))
	}
}
