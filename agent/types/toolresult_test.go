package types

import (
	"strings"
	"testing"
)

// TestFormatToolResultPlainString verifies that plain-text tool output (Read,
// Write, Edit) is passed through verbatim instead of JSON-quoted, so line
// numbers and tabs remain readable in the model observation.
func TestFormatToolResultPlainString(t *testing.T) {
	in := "1\tfunc main() {}\n2\treturn a + b"
	got := FormatToolResult(in)
	if got != in {
		t.Fatalf("FormatToolResult = %q, want verbatim %q", got, in)
	}
	if strings.Contains(got, `\n`) || strings.Contains(got, `\t`) {
		t.Fatalf("plain text was escaped: %q", got)
	}
}

func TestFormatToolResultEmptyString(t *testing.T) {
	if got := FormatToolResult("   "); got != ToolEmptyResultMessage {
		t.Fatalf("FormatToolResult(blank) = %q, want %q", got, ToolEmptyResultMessage)
	}
}

func TestFormatToolResultMapStillJSON(t *testing.T) {
	got := FormatToolResult(map[string]interface{}{"ok": true})
	if !strings.Contains(got, `"ok"`) {
		t.Fatalf("map result should stay JSON, got %q", got)
	}
}
