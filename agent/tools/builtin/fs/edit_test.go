package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeEditFile(t *testing.T, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, p
}

func readEditFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditFileSingleHunk(t *testing.T) {
	ws, p := writeEditFile(t, "hello world\nfoo bar\n")
	tool := NewEditTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    "f.txt",
		"old_str": "hello",
		"new_str": "goodbye",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]interface{}); m["message"] == "" {
		t.Error("missing message")
	}
	if got := readEditFile(t, p); got != "goodbye world\nfoo bar\n" {
		t.Errorf("content = %q", got)
	}
}

func TestEditFileMultiHunk(t *testing.T) {
	ws, p := writeEditFile(t, "alpha\nbeta\ngamma\n")
	tool := NewEditTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "f.txt",
		"hunks": []interface{}{
			map[string]interface{}{"old_str": "alpha", "new_str": "A"},
			map[string]interface{}{"old_str": "gamma", "new_str": "G"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, p); got != "A\nbeta\nG\n" {
		t.Errorf("content = %q, want %q", got, "A\nbeta\nG\n")
	}
}

func TestEditFileMultiHunkAtomicOnFailure(t *testing.T) {
	ws, p := writeEditFile(t, "alpha\nbeta\n")
	tool := NewEditTool(ws)
	// Second hunk not found → whole batch must fail, first hunk not applied.
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "f.txt",
		"hunks": []interface{}{
			map[string]interface{}{"old_str": "alpha", "new_str": "A"},
			map[string]interface{}{"old_str": "missing", "new_str": "X"},
		},
	})
	if err == nil {
		t.Fatal("expected error when a hunk is not found")
	}
	if got := readEditFile(t, p); got != "alpha\nbeta\n" {
		t.Errorf("file must be untouched on atomic failure, got %q", got)
	}
}

func TestEditFileHunksNoOverlapValidation(t *testing.T) {
	ws, p := writeEditFile(t, "abcabc\n")
	tool := NewEditTool(ws)
	// Both hunks match "abc" — first replaces first occurrence, second replaces
	// the second (they don't overlap in the original since Replace is sequential
	// and consumes the first). Applied = both, no error.
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "f.txt",
		"hunks": []interface{}{
			map[string]interface{}{"old_str": "abc", "new_str": "X"},
			map[string]interface{}{"old_str": "abc", "new_str": "Y"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, p); got != "XY\n" {
		t.Errorf("content = %q, want %q", got, "XY\n")
	}
}
