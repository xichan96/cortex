package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadFileFull(t *testing.T) {
	ws := writeTestFile(t, "line1\nline2\nline3\n")
	tool := NewReadTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]interface{})
	if !ok || m["content"] != "line1\nline2\nline3\n" {
		t.Fatalf("full read wrong: %v", res)
	}
}

func TestReadFileLineWindow(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\nl3\nl4\nl5\nl6\n")
	tool := NewReadTool(ws)

	// offset=2, limit=3 → lines 2-4.
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "test.txt", "offset": float64(2), "limit": float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["content"] != "l2\nl3\nl4" {
		t.Errorf("window content = %q, want %q", m["content"], "l2\nl3\nl4")
	}
	if m["total_lines"] != float64(6) && m["total_lines"] != 6 {
		t.Errorf("total_lines = %v, want 6", m["total_lines"])
	}
	if m["offset"] != float64(2) && m["offset"] != 2 {
		t.Errorf("offset = %v, want 2", m["offset"])
	}
	if m["truncated"] != true {
		t.Errorf("truncated = %v, want true", m["truncated"])
	}
}

func TestReadFileOffsetOnly(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\nl3\nl4\n")
	tool := NewReadTool(ws)

	// offset=3, no limit → lines 3-4.
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "test.txt", "offset": float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["content"] != "l3\nl4" {
		t.Errorf("content = %q, want %q", m["content"], "l3\nl4")
	}
}

func TestReadFileOffsetBeyondEOF(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\n")
	tool := NewReadTool(ws)

	// offset beyond total lines → empty window, total_lines reported.
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "test.txt", "offset": float64(99), "limit": float64(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["content"] != "" {
		t.Errorf("content = %q, want empty", m["content"])
	}
	if m["total_lines"] != float64(2) && m["total_lines"] != 2 {
		t.Errorf("total_lines = %v, want 2", m["total_lines"])
	}
}
