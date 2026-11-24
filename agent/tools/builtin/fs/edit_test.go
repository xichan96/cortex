package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		"file_path":  "f.txt",
		"old_string": "hello",
		"new_string": "goodbye",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "The file f.txt has been updated successfully."; res != want {
		t.Errorf("result = %q, want %q", res, want)
	}
	if got := readEditFile(t, p); got != "goodbye world\nfoo bar\n" {
		t.Errorf("content = %q", got)
	}
}

func TestEditFileRejectsAmbiguousOldString(t *testing.T) {
	ws, p := writeEditFile(t, "foo foo\nbar\n")
	tool := NewEditTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "f.txt",
		"old_string": "foo",
		"new_string": "baz",
	})
	if err == nil {
		t.Fatal("expected error for non-unique old_string")
	}
	if !strings.Contains(err.Error(), "2 matches") {
		t.Errorf("error should report match count, got %q", err.Error())
	}
	if got := readEditFile(t, p); got != "foo foo\nbar\n" {
		t.Errorf("file must be untouched, got %q", got)
	}
}

func TestEditFileRejectsMissingOldString(t *testing.T) {
	ws, p := writeEditFile(t, "alpha\n")
	tool := NewEditTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "f.txt",
		"old_string": "nope",
		"new_string": "x",
	})
	if err == nil {
		t.Fatal("expected error for missing old_string")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not-found", err.Error())
	}
	if got := readEditFile(t, p); got != "alpha\n" {
		t.Errorf("file must be untouched, got %q", got)
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	ws, p := writeEditFile(t, "foo foo\nfoo\n")
	tool := NewEditTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":   "f.txt",
		"old_string":  "foo",
		"new_string":  "bar",
		"replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, p); got != "bar bar\nbar\n" {
		t.Errorf("content = %q", got)
	}
}

func TestEditFileMultiHunk(t *testing.T) {
	ws, p := writeEditFile(t, "alpha\nbeta\ngamma\n")
	tool := NewEditTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "f.txt",
		"hunks": []interface{}{
			map[string]interface{}{"old_string": "alpha", "new_string": "A"},
			map[string]interface{}{"old_string": "gamma", "new_string": "G"},
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
		"file_path": "f.txt",
		"hunks": []interface{}{
			map[string]interface{}{"old_string": "alpha", "new_string": "A"},
			map[string]interface{}{"old_string": "missing", "new_string": "X"},
		},
	})
	if err == nil {
		t.Fatal("expected error when a hunk is not found")
	}
	if got := readEditFile(t, p); got != "alpha\nbeta\n" {
		t.Errorf("file must be untouched on atomic failure, got %q", got)
	}
}

func TestEditFileMissing(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "absent.txt",
		"old_string": "a",
		"new_string": "b",
	})
	if err == nil || !strings.Contains(err.Error(), "File does not exist: absent.txt") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditToolNameSchemaAndMetadata(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	if tool.Name() != "Edit" {
		t.Errorf("Name = %q, want Edit", tool.Name())
	}
	props := tool.Schema()["properties"].(map[string]interface{})
	for _, key := range []string{"file_path", "old_string", "new_string", "replace_all"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q", key)
		}
	}
	extra := tool.Metadata().Extra
	if v, ok := extra["no_cache"].(bool); !ok || !v {
		t.Errorf("Edit metadata no_cache = %v, want true", extra["no_cache"])
	}
	if v, ok := extra["invalidates_cache"].(bool); !ok || !v {
		t.Errorf("Edit metadata invalidates_cache = %v, want true", extra["invalidates_cache"])
	}
}
