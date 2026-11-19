package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func TestGoldenJSONLRoundTrip(t *testing.T) {
	goldenPath := filepath.Join("testdata", "golden.jsonl")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	wantLines := splitLines(string(raw))
	if len(wantLines) != 14 { // header + 13 entries
		t.Fatalf("golden has %d lines, want 14", len(wantLines))
	}

	src := filepath.Join(t.TempDir(), "src.jsonl")
	if err := os.WriteFile(src, raw, 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	l, err := Open(src, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer l.Close()

	if got := l.Header().ID; got != "sess-golden" {
		t.Fatalf("header id = %q", got)
	}
	if got := l.Len(); got != 13 {
		t.Fatalf("entries = %d, want 13", got)
	}
	if got := l.Leaf(); got != "m2" {
		t.Fatalf("leaf = %q, want m2", got)
	}
	if diags := l.Diagnostics(); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	wantTypes := []EntryType{
		EntryMessage, EntryModelChange, EntryThinking, EntryMessage, EntryMessage,
		EntryUsage, EntryCustom, EntryCustomMessage, EntryLabel, EntrySessionInfo,
		EntryBranchSummary, EntryCompaction, EntryMessage,
	}
	for i, e := range l.Entries() {
		if e.EntryType() != wantTypes[i] {
			t.Errorf("entry %d type = %q, want %q", i, e.EntryType(), wantTypes[i])
		}
		if e.Base().Type != wantTypes[i] {
			t.Errorf("entry %d base type = %q, want %q", i, e.Base().Type, wantTypes[i])
		}
	}

	// Re-encode every entry into a fresh session and compare JSON semantically.
	dst := filepath.Join(t.TempDir(), "dst.jsonl")
	out, err := Open(dst, OpenOptions{Create: true, Header: Header{CWD: "/tmp/project"}})
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	for _, e := range l.Entries() {
		if _, err := out.Append(e); err != nil {
			t.Fatalf("append %s: %v", e.Base().ID, err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close dst: %v", err)
	}

	dstRaw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	gotLines := splitLines(string(dstRaw))
	if len(gotLines) != len(wantLines) {
		t.Fatalf("round-trip lines = %d, want %d", len(gotLines), len(wantLines))
	}
	for i := 1; i < len(wantLines); i++ { // skip differing header
		var want, got any
		if err := json.Unmarshal([]byte(wantLines[i]), &want); err != nil {
			t.Fatalf("golden line %d: %v", i+1, err)
		}
		if err := json.Unmarshal([]byte(gotLines[i]), &got); err != nil {
			t.Fatalf("round-trip line %d: %v", i+1, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("line %d mismatch:\nwant %s\ngot  %s", i+1, wantLines[i], gotLines[i])
		}
	}
}

func TestMessagePartsRoundTrip(t *testing.T) {
	msg := types.Message{
		Role: "user",
		Parts: []types.MessagePart{
			types.TextPart{Text: "look"},
			types.ImageURLPart{URL: "https://example.test/a.png", Detail: "auto"},
			types.ImageDataPart{Data: []byte{0, 1, 2, 3}, MIMEType: "image/png"},
		},
		Usage: types.Usage{PromptTokens: 3, TotalTokens: 7},
	}
	raw, err := marshalMessage(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := unmarshalMessage(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, msg) {
		t.Fatalf("message mismatch:\nwant %#v\ngot  %#v", msg, got)
	}
}

func TestMessageContentArrayIsAccepted(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"plain"}]}`)
	got, err := unmarshalMessage(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Parts) != 1 {
		t.Fatalf("parts = %#v", got.Parts)
	}
	if text, ok := got.Parts[0].(types.TextPart); !ok || text.Text != "plain" {
		t.Fatalf("part = %#v", got.Parts[0])
	}
}

func TestUnknownEntryTypeIsReported(t *testing.T) {
	_, err := UnmarshalEntry([]byte(`{"type":"nope","id":"x","parentId":null,"timestamp":"2025-01-01T00:00:00Z"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	var unknown *UnknownEntryTypeError
	if !strings.Contains(err.Error(), "unknown entry type") {
		t.Fatalf("error = %v", err)
	}
	if !errors.As(err, &unknown) {
		t.Fatalf("error type = %T", err)
	}
}

func TestUnknownContentBlockIsReported(t *testing.T) {
	entry := `{"type":"message","id":"x","parentId":null,"timestamp":"2025-01-01T00:00:00Z","message":{"role":"user","content":"","parts":[{"type":"thinking","text":"hmm"}]}}`
	_, err := UnmarshalEntry([]byte(entry))
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("error = %v", err)
	}
}

func TestEntryMissingIDIsRejected(t *testing.T) {
	_, err := UnmarshalEntry([]byte(`{"type":"session_info","parentId":null,"timestamp":"2025-01-01T00:00:00Z","name":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("error = %v", err)
	}
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
