package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTruncatesWithinBudget(t *testing.T) {
	big := strings.Repeat("a", 5000)
	tool := newTool("big", func(context.Context, map[string]interface{}) (interface{}, error) {
		return big, nil
	})
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 800}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "big", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	text := r.Text()
	if len(text) > 800 {
		t.Fatalf("truncated length = %d, want <= 800", len(text))
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("missing omission marker: %q", text)
	}
	if !strings.Contains(text, "Output:") {
		t.Fatalf("missing header guide: %q", text)
	}
	if !strings.Contains(text, "original_bytes: 5000") {
		t.Fatalf("missing header metadata: %q", text)
	}
}

func TestSanitizeRespectsToolBudgetOverride(t *testing.T) {
	tool := newTool("big", func(context.Context, map[string]interface{}) (interface{}, error) {
		return strings.Repeat("x", 4000), nil
	})
	tool.meta.MaxTruncationLength = 120
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 5000}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "big", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if len(r.Text()) > 120 {
		t.Fatalf("length = %d, want <= 120 (tool budget)", len(r.Text()))
	}
}

func TestSanitizeIsUTF8Safe(t *testing.T) {
	big := strings.Repeat("工具管线", 800)
	tool := newTool("big", func(context.Context, map[string]interface{}) (interface{}, error) {
		return big, nil
	})
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 257}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "big", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	text := r.Text()
	if len(text) > 257 {
		t.Fatalf("length = %d, want <= 257", len(text))
	}
	if !utf8.ValidString(text) {
		t.Fatalf("truncation split a rune")
	}
}

func TestSanitizeSpillsOversizedOutput(t *testing.T) {
	big := strings.Repeat("z", 20000)
	tool := newTool("big", func(context.Context, map[string]interface{}) (interface{}, error) {
		return big, nil
	})
	var spilledCallID string
	spill := SpillWriterFunc(func(_ context.Context, c *Call, content string) (string, error) {
		spilledCallID = c.ID
		if content != big {
			t.Errorf("spill received %d bytes, want full %d", len(content), len(big))
		}
		return "/tmp/big-output.txt", nil
	})
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 500, Spill: spill}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "big", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if spilledCallID != "c1" {
		t.Fatalf("spill call id = %q", spilledCallID)
	}
	text := r.Text()
	if len(text) > 500 {
		t.Fatalf("length = %d, want <= 500", len(text))
	}
	if !strings.Contains(text, "Full output saved to") {
		t.Fatalf("missing spill hint: %q", text)
	}
}

func TestSanitizeDoesNotSpillWhenSpillFails(t *testing.T) {
	big := strings.Repeat("z", 20000)
	tool := newTool("big", func(context.Context, map[string]interface{}) (interface{}, error) {
		return big, nil
	})
	spill := SpillWriterFunc(func(context.Context, *Call, string) (string, error) {
		return "", os.ErrPermission
	})
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 200, Spill: spill}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "big", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	text := r.Text()
	if len(text) > 200 {
		t.Fatalf("length = %d, want <= 200", len(text))
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("expected in-context fallback truncation: %q", text)
	}
}

func TestSmallResultsAreNotTruncated(t *testing.T) {
	tool := newTool("small", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "short", nil
	})
	p := newTestPipeline(t, Config{Truncation: Truncation{DefaultMaxBytes: 10}})
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "small", tool, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if got := r.Text(); got != "short" {
		t.Fatalf("text = %q, want verbatim", got)
	}
}

func TestDirSpillWriterSanitizesFileName(t *testing.T) {
	dir := t.TempDir()
	w := DirSpillWriter{Dir: dir}
	path, err := w.Spill(context.Background(), &Call{ID: "../../evil/id"}, "body")
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("spill escaped dir: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != "body" {
		t.Fatalf("spill content = %q", data)
	}
}
