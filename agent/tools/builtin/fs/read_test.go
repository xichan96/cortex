package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	res, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "1\tline1\n2\tline2\n3\tline3"
	if res != want {
		t.Fatalf("read = %q, want %q", res, want)
	}
}

func TestReadFileWithoutTrailingNewline(t *testing.T) {
	ws := writeTestFile(t, "a\nb")
	tool := NewReadTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res != "1\ta\n2\tb" {
		t.Fatalf("read = %q", res)
	}
}

func TestReadEmptyFile(t *testing.T) {
	ws := writeTestFile(t, "")
	tool := NewReadTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res != "" {
		t.Fatalf("read empty file = %q, want empty", res)
	}
}

func TestReadFileLineWindow(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\nl3\nl4\nl5\nl6\n")
	tool := NewReadTool(ws)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "test.txt", "offset": float64(2), "limit": float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(string)
	want := "2\tl2\n3\tl3\n4\tl4"
	if !strings.HasPrefix(got, want) {
		t.Errorf("window = %q, want prefix %q", got, want)
	}
	if !strings.Contains(got, "Use offset=5 to continue") {
		t.Errorf("window missing continuation hint: %q", got)
	}
}

func TestReadFileWindowToEOFHasNoHint(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\nl3\nl4\n")
	tool := NewReadTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "test.txt", "offset": float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != "3\tl3\n4\tl4" {
		t.Errorf("content = %q", res)
	}
}

func TestReadFileOffsetBeyondEOF(t *testing.T) {
	ws := writeTestFile(t, "l1\nl2\n")
	tool := NewReadTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "test.txt", "offset": float64(99), "limit": float64(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != "" {
		t.Errorf("content = %q, want empty", res)
	}
}

func TestReadFileMissing(t *testing.T) {
	ws := t.TempDir()
	tool := NewReadTool(ws)
	_, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "missing.txt"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if want := "File does not exist: missing.txt"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestReadFileRequiresFilePath(t *testing.T) {
	tool := NewReadTool(t.TempDir())
	if _, err := tool.Execute(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("expected error when file_path is missing")
	}
}

func TestReadToolNameSchemaAndMetadata(t *testing.T) {
	tool := NewReadTool(t.TempDir())
	if tool.Name() != "Read" {
		t.Errorf("Name = %q, want Read", tool.Name())
	}
	schema := tool.Schema()
	props := schema["properties"].(map[string]interface{})
	for _, key := range []string{"file_path", "offset", "limit"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q", key)
		}
	}
	req := schema["required"].([]string)
	if len(req) != 1 || req[0] != "file_path" {
		t.Errorf("required = %v, want [file_path]", req)
	}
	extra := tool.Metadata().Extra
	if v, ok := extra["no_cache"].(bool); !ok || !v {
		t.Errorf("Read metadata no_cache = %v, want true", extra["no_cache"])
	}
}

func TestReadRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(dir)
	_, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "blob.bin"})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("Read(binary) error = %v, want binary error", err)
	}
}
