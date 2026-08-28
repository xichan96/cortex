package types

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateMiddle_KeepsHeadAndTail(t *testing.T) {
	s := strings.Repeat("a", 1000) + "MIDDLE" + strings.Repeat("b", 1000)
	out, omitted := TruncateMiddle(s, 512)
	if len(out) > 512 {
		t.Fatalf("output exceeds budget: %d > 512", len(out))
	}
	// 50/50 split of the body budget (after the marker).
	if !strings.HasPrefix(out, "a") || !strings.HasSuffix(out, "b") {
		t.Error("head and tail not preserved")
	}
	if !strings.Contains(out, "…truncated…") {
		t.Error("omission marker missing")
	}
	// Both the a-run and b-run should be substantial (> 100 bytes each).
	headPart := out[:len(out)-strings.LastIndex(out, "\n…")]
	if len(headPart) < 100 {
		t.Errorf("head too short: %d", len(headPart))
	}
	if omitted <= 0 {
		t.Errorf("expected omitted > 0, got %d", omitted)
	}
}

func TestTruncateMiddle_UTF8Boundary(t *testing.T) {
	// CJK + emoji straddling the budget boundary; the result must stay valid UTF-8.
	s := strings.Repeat("界", 300) + "🎉" + strings.Repeat("文", 300)
	out, _ := TruncateMiddle(s, 512)
	if !utf8.ValidString(out) {
		t.Fatal("output is not valid UTF-8")
	}
	if len(out) > 512 {
		t.Fatalf("output exceeds budget: %d > 512", len(out))
	}
}

func TestTruncateMiddle_ShortInputNoOp(t *testing.T) {
	s := "short"
	out, omitted := TruncateMiddle(s, 512)
	if out != s {
		t.Errorf("expected unchanged, got %q", out)
	}
	if omitted != 0 {
		t.Errorf("expected omitted 0, got %d", omitted)
	}
}

func TestBuildOutputHeader_FieldsOmitted(t *testing.T) {
	code := 3
	h := OutputHeader{
		ChunkID:        "call_1",
		ExitCode:       &code,
		OriginalBytes:  100,
		OriginalTokens: 25,
		TotalLines:     2,
	}
	hd := BuildOutputHeader(h)
	if !strings.Contains(hd, "chunk_id: call_1\n") {
		t.Errorf("missing chunk_id line: %q", hd)
	}
	if !strings.Contains(hd, "exit_code: 3\n") {
		t.Errorf("missing exit_code line: %q", hd)
	}
	if !strings.Contains(hd, "original_bytes: 100\n") {
		t.Errorf("missing original_bytes line: %q", hd)
	}
	// WallTime and SavedPath are zero → omitted.
	if strings.Contains(hd, "wall_time") {
		t.Errorf("wall_time should be omitted: %q", hd)
	}
	if strings.Contains(hd, "saved_path") {
		t.Errorf("saved_path should be omitted: %q", hd)
	}
	if !strings.HasSuffix(hd, "Output:\n") {
		t.Errorf("Output: guide line must be last: %q", hd)
	}
}

func TestTruncateToolResult_ReservesHeader(t *testing.T) {
	content := strings.Repeat("content-", 2000)
	code := 0
	header := OutputHeader{
		ChunkID:        "call_xyz",
		ExitCode:       &code,
		OriginalBytes:  len(content),
		OriginalTokens: RoughTokenEstimate(content),
		TotalLines:     1,
	}
	display, meta := TruncateToolResult(content, 1000, "", header)
	if !meta.Truncated {
		t.Fatal("expected truncated")
	}
	if len(display) > 1000 {
		t.Fatalf("display exceeds maxLen: %d > 1000", len(display))
	}
	if !strings.Contains(display, "Output:") {
		t.Error("Output: guide line missing")
	}
	if !strings.Contains(display, "…truncated…") {
		t.Error("omission marker missing")
	}
}

func TestTruncateToolResult_WriteFile(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("big-output-line\n", 3000) // > 2048*8
	header := OutputHeader{ChunkID: "call_w"}
	display, meta := TruncateToolResult(content, 2048, dir, header)
	if !meta.Truncated {
		t.Fatal("expected truncated")
	}
	if meta.SavedFilePath == "" {
		t.Fatal("expected a saved file path")
	}
	if _, err := os.Stat(meta.SavedFilePath); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	if !strings.Contains(display, "Full output saved to:") {
		t.Errorf("expected saved hint, got %q", display)
	}
	// The body must not appear in the display.
	if strings.Contains(display, "big-output-line") {
		t.Errorf("body leaked into display: %q", display)
	}
	if len(display) > 2048 {
		t.Fatalf("display exceeds maxLen: %d > 2048", len(display))
	}
}

func TestTruncateToolResult_HeaderBudgetCapped(t *testing.T) {
	// A long ChunkID + many fields: header would exceed maxLen/4; the body must
	// still keep maxLen/4 and the display stay within budget.
	content := strings.Repeat("z", 5000)
	code := 1
	header := OutputHeader{
		ChunkID:        strings.Repeat("id-", 500),
		ExitCode:       &code,
		WallTime:       123456789,
		OriginalBytes:  len(content),
		OriginalTokens: RoughTokenEstimate(content),
		TotalLines:     100,
	}
	maxLen := 256
	display, _ := TruncateToolResult(content, maxLen, "", header)
	if len(display) > maxLen {
		t.Fatalf("display exceeds maxLen: %d > %d", len(display), maxLen)
	}
	// Body must keep at least maxLen/4 bytes.
	if len(display) < maxLen/4 {
		t.Fatalf("body lost too much to header: display %d bytes", len(display))
	}
}

func TestSanitizeToolResult_MiddlePreserved(t *testing.T) {
	in := map[string]interface{}{
		"name": strings.Repeat("a", 500) + "KEEP" + strings.Repeat("b", 500),
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": strings.Repeat("c", 500) + "TAIL" + strings.Repeat("d", 500)},
			map[string]interface{}{"type": "image", "data": "base64long", "media_type": "image/png"},
		},
	}
	out := SanitizeToolResult(in, 512)
	m := out.(map[string]interface{})
	if s := m["name"].(string); !strings.Contains(s, "…truncated…") {
		t.Errorf("name middle not preserved: %q", s)
	}
	content := m["content"].([]interface{})
	textItem := content[0].(map[string]interface{})
	if s := textItem["text"].(string); !strings.Contains(s, "…truncated…") {
		t.Errorf("content text middle not preserved: %q", s)
	}
	imgItem := content[1].(map[string]interface{})
	if _, hasData := imgItem["data"]; hasData {
		t.Error("image data should be stripped")
	}
	if imgItem["omitted"] != true {
		t.Error("image should be marked omitted")
	}
}

func TestTruncateString_UTF8Safe(t *testing.T) {
	s := "中文测试" + strings.Repeat("x", 100)
	out := TruncateString(s, 12)
	if !utf8.ValidString(out) {
		t.Fatal("TruncateString output not valid UTF-8")
	}
	if !strings.HasSuffix(out, "...") {
		t.Error("expected ... suffix")
	}
}

func TestTruncateToolResult_NoTruncationShortContent(t *testing.T) {
	content := "small result"
	display, meta := TruncateToolResult(content, 2048, "", OutputHeader{})
	if meta.Truncated {
		t.Error("short content should not be marked truncated")
	}
	if display != content {
		t.Errorf("expected verbatim content, got %q", display)
	}
}

func TestTruncateToolResult_WriteFileTinyBudgetKeepsPath(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("y", 50000)
	header := OutputHeader{ChunkID: "call_tiny"}
	display, meta := TruncateToolResult(content, 128, dir, header)
	if !meta.Truncated {
		t.Fatal("expected truncated")
	}
	if meta.SavedFilePath == "" {
		t.Fatal("expected saved file path")
	}
	if len(display) > 128 {
		t.Fatalf("display exceeds tiny budget: %d > 128", len(display))
	}
	if !strings.Contains(display, "saved to:") {
		t.Errorf("expected saved hint in display, got %q", display)
	}
}
