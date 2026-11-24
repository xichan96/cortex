package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileCreated(t *testing.T) {
	ws := t.TempDir()
	tool := NewWriteTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "new.txt",
		"content":   "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "File created successfully at: new.txt"; res != want {
		t.Errorf("result = %q, want %q", res, want)
	}
	got, err := os.ReadFile(filepath.Join(ws, "new.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("file content = %q, err = %v", got, err)
	}
}

func TestWriteFileUpdated(t *testing.T) {
	ws := writeTestFile(t, "old\n")
	tool := NewWriteTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "test.txt",
		"content":   "new\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "The file test.txt has been updated successfully."; res != want {
		t.Errorf("result = %q, want %q", res, want)
	}
}

func TestWriteFileCreatesParentDirs(t *testing.T) {
	ws := t.TempDir()
	tool := NewWriteTool(ws)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "a/b/c.txt",
		"content":   "x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "a", "b", "c.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteToolNameSchemaAndMetadata(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	if tool.Name() != "Write" {
		t.Errorf("Name = %q, want Write", tool.Name())
	}
	props := tool.Schema()["properties"].(map[string]interface{})
	for _, key := range []string{"file_path", "content"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q", key)
		}
	}
	extra := tool.Metadata().Extra
	if v, ok := extra["no_cache"].(bool); !ok || !v {
		t.Errorf("Write metadata no_cache = %v, want true", extra["no_cache"])
	}
	if v, ok := extra["invalidates_cache"].(bool); !ok || !v {
		t.Errorf("Write metadata invalidates_cache = %v, want true", extra["invalidates_cache"])
	}
}

// TestReadAfterWriteSeesNewContent is the regression for the P0 "write then
// read stale content" bug: a Read after Write/Edit must return the new bytes.
func TestReadAfterWriteSeesNewContent(t *testing.T) {
	ws := writeTestFile(t, "A\n")
	read := NewReadTool(ws)
	write := NewWriteTool(ws)
	ctx := context.Background()

	first, err := read.Execute(ctx, map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if first != "1\tA" {
		t.Fatalf("first read = %q", first)
	}

	if _, err := write.Execute(ctx, map[string]interface{}{"file_path": "test.txt", "content": "B\n"}); err != nil {
		t.Fatal(err)
	}

	second, err := read.Execute(ctx, map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if second != "1\tB" {
		t.Fatalf("read after write = %q, want fresh content", second)
	}
}

func TestReadAfterEditSeesNewContent(t *testing.T) {
	ws := writeTestFile(t, "A\n")
	read := NewReadTool(ws)
	edit := NewEditTool(ws)
	ctx := context.Background()

	if _, err := read.Execute(ctx, map[string]interface{}{"file_path": "test.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Execute(ctx, map[string]interface{}{
		"file_path":  "test.txt",
		"old_string": "A",
		"new_string": "B",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := read.Execute(ctx, map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if second != "1\tB" {
		t.Fatalf("read after edit = %q, want fresh content", second)
	}
}

func TestReadRejectsDirectory(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "."})
	if err == nil || !strings.Contains(err.Error(), "Use Glob") {
		t.Fatalf("Read(directory) error = %v, want Glob hint", err)
	}
}
