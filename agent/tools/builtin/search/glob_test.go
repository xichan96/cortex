package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDsMatch(t *testing.T) {
	cases := []struct {
		rel, pat string
		want     bool
	}{
		{"a/b/c.go", "**.go", true},         // ** at start
		{"c.go", "**.go", true},             // ** matches zero segments
		{"a/b/c.go", "a/**/c.go", true},     // ** in middle
		{"a/c.go", "a/**/c.go", true},       // ** matches zero segments
		{"a/b/d/c.go", "a/**/c.go", true},   // deep
		{"a/b/d/e.txt", "a/**/c.go", false}, // wrong file
		{"a/b/c.go", "a/*/c.go", true},      // single *
		{"a/b/c.go", "a/*/*.go", true},      // two single *
		{"a/b/d/c.go", "a/*/*.go", false},   // too deep for two *
		{"src/main.go", "src/**/*.go", true},
		{"src/main.go", "**/*.go", true},
		{"main.go", "**/*.go", true},
	}
	for _, c := range cases {
		if got := dsMatch(c.rel, c.pat); got != c.want {
			t.Errorf("dsMatch(%q, %q) = %v, want %v", c.rel, c.pat, got, c.want)
		}
	}
}

func TestGlobDoubleStar(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a/b/c.go", "pkg c")
	mustWrite("a/d.go", "pkg d")
	mustWrite("e.txt", "txt")

	pattern := filepath.Join(dir, "**", "*.go")
	got := globDoubleStar(pattern)
	if len(got) != 2 {
		t.Fatalf("glob %q = %d matches, want 2: %v", pattern, len(got), got)
	}
}
