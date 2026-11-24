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
	writeFixture(t, dir, "sample.txt", "alpha\nbeta\nalpha gamma\n")
	return dir, NewGrepTool(dir).(*GrepTool)
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGrep(t *testing.T, tool *GrepTool, input map[string]interface{}) string {
	t.Helper()
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Grep(%v): %v", input, err)
	}
	return res.(string)
}

func TestGrep_NameAndNoCache(t *testing.T) {
	tool := NewGrepTool(t.TempDir()).(*GrepTool)
	if got := tool.Name(); got != "Grep" {
		t.Fatalf("Name() = %q, want Grep", got)
	}
	md := tool.Metadata()
	if v, _ := md.Extra["no_cache"].(bool); !v {
		t.Fatalf("Grep metadata must set no_cache=true, got %+v", md.Extra)
	}
}

func TestGrep_ContentModeDefault(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha"})
	// Two matching lines, relative path, no line numbers without -n.
	for _, want := range []string{"sample.txt:alpha", "sample.txt:alpha gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("content output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sample.txt:1:") {
		t.Errorf("line numbers must be omitted without -n; got:\n%s", out)
	}
}

func TestGrep_ContentModeLineNumbers(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "-n": true})
	want := []string{"sample.txt:1:alpha", "sample.txt:3:alpha gamma"}
	got := strings.Split(out, "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines (%q), want %d", len(got), out, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGrep_ContentModeOrderAndRelativePaths(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, "aaa/first.go", "alpha one\n")
	writeFixture(t, dir, "zzz/last.go", "alpha two\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "-n": true})
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %q", out)
	}
	if !strings.HasPrefix(lines[0], "aaa/first.go:1:") {
		t.Errorf("first line must be sorted relative path, got %q", lines[0])
	}
	if strings.HasPrefix(lines[len(lines)-1], "/") || strings.Contains(lines[len(lines)-1], dir) {
		t.Errorf("paths must be relative to workspace, got %q", lines[len(lines)-1])
	}
}

func TestGrep_CountMode(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "output_mode": "count"})
	if !strings.Contains(out, "sample.txt:2") {
		t.Errorf("count output missing per-file count; got:\n%s", out)
	}
	if !strings.Contains(out, "Found 2 total occurrences across 1 files.") {
		t.Errorf("count summary missing; got:\n%s", out)
	}
}

func TestGrep_FilesWithMatchesMode(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, "other.txt", "nothing here\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "output_mode": "files_with_matches"})
	if out != "sample.txt" {
		t.Fatalf("files_with_matches = %q, want just sample.txt", out)
	}
}

func TestGrep_NoMatchReturnsEmptyNotError(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "zzz-no-such-pattern"})
	if out != "" {
		t.Fatalf("no-match must return empty string, got %q", out)
	}
}

func TestGrep_BadPathReturnsError(t *testing.T) {
	_, tool := newGrepFixture(t)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "x", "path": "/does/not/exist"}); err == nil {
		t.Fatal("Grep on a missing path must error")
	}
}

func TestGrep_GlobFilter(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, "pkg/a.go", "alpha in go\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "Glob": "*.go"})
	if strings.Contains(out, "sample.txt") {
		t.Errorf("Glob filter must exclude sample.txt; got:\n%s", out)
	}
	if !strings.Contains(out, "pkg/a.go") {
		t.Errorf("Glob filter must include pkg/a.go; got:\n%s", out)
	}
}

func TestGrep_TypeFilter(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, "pkg/a.go", "alpha in go\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "type": "go"})
	if !strings.Contains(out, "pkg/a.go") || strings.Contains(out, "sample.txt") {
		t.Errorf("type=go must only match .go files; got:\n%s", out)
	}
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "x", "type": "nope"}); err == nil {
		t.Error("unknown type must error")
	}
}

func TestGrep_CaseInsensitive(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "ALPHA", "-i": true})
	if !strings.Contains(out, "alpha") {
		t.Fatalf("-i must match case-insensitively; got %q", out)
	}
	if out := runGrep(t, tool, map[string]interface{}{"pattern": "ALPHA"}); out != "" {
		t.Fatalf("case-sensitive search must not match, got %q", out)
	}
}

func TestGrep_OnlyMatching(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": `(alpha|beta)`, "-o": true, "-n": true})
	want := []string{"sample.txt:1:alpha", "sample.txt:2:beta", "sample.txt:3:alpha"}
	got := strings.Split(out, "\n")
	if len(got) != len(want) {
		t.Fatalf("-o output = %q, want %d matches", out, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("-o line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGrep_ContextLines(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "beta", "-n": true, "-C": 1})
	if !strings.Contains(out, "sample.txt:2:beta") {
		t.Errorf("matching line missing; got:\n%s", out)
	}
	if !strings.Contains(out, "sample.txt-1-alpha") {
		t.Errorf("leading context missing; got:\n%s", out)
	}
	if !strings.Contains(out, "sample.txt-3-alpha gamma") {
		t.Errorf("trailing context missing; got:\n%s", out)
	}
}

func TestGrep_HeadLimit(t *testing.T) {
	_, tool := newGrepFixture(t)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "-n": true, "head_limit": 1})
	if strings.Contains(out, "\n") {
		t.Fatalf("head_limit=1 must return one entry, got:\n%s", out)
	}
}

func TestGrep_Multiline(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "multi.txt", "foo\nbar\nbaz\n")
	tool := NewGrepTool(dir).(*GrepTool)
	out := runGrep(t, tool, map[string]interface{}{"pattern": "foo\nbar", "multiline": true, "-n": true})
	if !strings.Contains(out, "multi.txt:1:foo") {
		t.Fatalf("multiline match must report the starting line; got %q", out)
	}
	// Without multiline the same pattern does not match.
	if out := runGrep(t, tool, map[string]interface{}{"pattern": "foo\nbar"}); out != "" {
		t.Fatalf("non-multiline search must not match across lines, got %q", out)
	}
}

func TestGrep_InvalidOutputMode(t *testing.T) {
	_, tool := newGrepFixture(t)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "x", "output_mode": "bogus"}); err == nil {
		t.Fatal("invalid output_mode must error")
	}
}

// TestGrepIgnoresVendorDir 端到端：vendor 目录内容不应出现在 Grep 结果里。
func TestGrepIgnoresVendorDir(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, "vendor/dep/lib.go", "alpha secret\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha", "path": "."})
	if strings.Contains(out, "vendor") {
		t.Errorf("Grep result must exclude vendor dir, got: %q", out)
	}
}

func TestGrepRespectsGitignore(t *testing.T) {
	dir, tool := newGrepFixture(t)
	writeFixture(t, dir, ".gitignore", "*.log\n")
	writeFixture(t, dir, "debug.log", "alpha logged\n")
	out := runGrep(t, tool, map[string]interface{}{"pattern": "alpha"})
	if strings.Contains(out, "debug.log") {
		t.Errorf("Grep must respect .gitignore, got: %q", out)
	}
}
