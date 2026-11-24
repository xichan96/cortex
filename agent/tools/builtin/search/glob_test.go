package search

import (
	"context"
	"path/filepath"
	"testing"
)

func newGlobFixture(t *testing.T) (string, *GlobTool) {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, "a/b/c.go", "package b")
	writeFixture(t, dir, "a/d.go", "package a")
	writeFixture(t, dir, "e.txt", "txt")
	writeFixture(t, dir, "pkg/nested/main.go", "package nested")
	return dir, NewGlobTool(dir).(*GlobTool)
}

func runGlob(t *testing.T, tool *GlobTool, input map[string]interface{}) string {
	t.Helper()
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Glob(%v): %v", input, err)
	}
	return res.(string)
}

func TestGlob_NameAndNoCache(t *testing.T) {
	tool := NewGlobTool(t.TempDir()).(*GlobTool)
	if got := tool.Name(); got != "Glob" {
		t.Fatalf("Name() = %q, want Glob", got)
	}
	if v, _ := tool.Metadata().Extra["no_cache"].(bool); !v {
		t.Fatalf("Glob metadata must set no_cache=true, got %+v", tool.Metadata().Extra)
	}
}

func TestGlob_DoubleStarRelativeAndSorted(t *testing.T) {
	_, tool := newGlobFixture(t)
	out := runGlob(t, tool, map[string]interface{}{"pattern": "**/*.go"})
	want := []string{"a/b/c.go", "a/d.go", "pkg/nested/main.go"}
	got := splitLines(out)
	if len(got) != len(want) {
		t.Fatalf("Glob = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGlob_BarePatternMatchesAnyDepth(t *testing.T) {
	_, tool := newGlobFixture(t)
	out := runGlob(t, tool, map[string]interface{}{"pattern": "*.go"})
	if len(splitLines(out)) != 3 {
		t.Fatalf("bare *.go must match at any depth, got %q", out)
	}
}

func TestGlob_PathRoot(t *testing.T) {
	_, tool := newGlobFixture(t)
	out := runGlob(t, tool, map[string]interface{}{"pattern": "**/*.go", "path": "pkg"})
	if out != "pkg/nested/main.go" {
		t.Fatalf("path=pkg Glob = %q, want only pkg/nested/main.go", out)
	}
}

func TestGlob_OnlyFilesNotDirectories(t *testing.T) {
	_, tool := newGlobFixture(t)
	out := runGlob(t, tool, map[string]interface{}{"pattern": "**"})
	for _, line := range splitLines(out) {
		if line == "a" || line == "a/b" || line == "pkg" {
			t.Errorf("Glob must not return directories, got %q", line)
		}
	}
}

func TestGlob_AbsolutePattern(t *testing.T) {
	dir, tool := newGlobFixture(t)
	out := runGlob(t, tool, map[string]interface{}{"pattern": filepath.Join(dir, "**", "*.go")})
	if len(splitLines(out)) != 3 {
		t.Fatalf("absolute pattern = %q, want 3 go files", out)
	}
}

func TestGlob_ExcludesVendorAndGitignored(t *testing.T) {
	dir, tool := newGlobFixture(t)
	writeFixture(t, dir, "vendor/dep/lib.go", "package dep")
	writeFixture(t, dir, ".gitignore", "pkg/nested/\n")
	out := runGlob(t, tool, map[string]interface{}{"pattern": "**/*.go"})
	if containsLine(out, "vendor/dep/lib.go") {
		t.Errorf("Glob must skip vendor, got %q", out)
	}
	if containsLine(out, "pkg/nested/main.go") {
		t.Errorf("Glob must respect .gitignore, got %q", out)
	}
}

func TestGlob_MissingPathErrors(t *testing.T) {
	tool := NewGlobTool(t.TempDir()).(*GlobTool)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "**/*.go", "path": "nope"}); err == nil {
		t.Fatal("Glob on a missing path must error")
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func containsLine(s, want string) bool {
	for _, line := range splitLines(s) {
		if line == want {
			return true
		}
	}
	return false
}
