package transcript

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func TestFindCutPointNeverCutsToolResult(t *testing.T) {
	l, _ := newTestLog(t)
	u1 := appendMsg(t, l, types.Message{Role: "user", Content: "a"})
	appendMsg(t, l, types.Message{Role: "assistant", ToolCalls: []types.ToolCall{
		{ID: "call-1", Type: "function", Function: types.ToolFunction{Name: "read", Arguments: map[string]any{"path": "x"}}},
	}})
	appendMsg(t, l, types.Message{Role: "tool", Content: "tool", ToolCallID: "call-1"})
	u2 := appendMsg(t, l, types.Message{Role: "user", Content: "b"})

	path, err := l.PathTo("")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if path[0].Base().ID != u1 || path[3].Base().ID != u2 {
		t.Fatalf("path ids = %v", pathIDs(path))
	}

	cut := FindCutPoint(path, 2)
	if cut.Index != 3 || cut.EntryID != u2 {
		t.Fatalf("cut = %+v, want user %s", cut, u2)
	}
	if !isCutPointEntry(path[cut.Index]) {
		t.Fatalf("cut entry is not a valid cut point: %T", path[cut.Index])
	}
	if cut.IsSplitTurn {
		t.Fatalf("cut = %+v", cut)
	}

	large := FindCutPoint(path, 10_000)
	if large.Index != 0 || large.EntryID != u1 {
		t.Fatalf("large keep cut = %+v", large)
	}
	if none := FindCutPoint(path, 0); none.Index != len(path) || none.EntryID != "" {
		t.Fatalf("retain-none cut = %+v", none)
	}
	if empty := FindCutPoint(nil, 10); empty.Index != -1 {
		t.Fatalf("empty cut = %+v", empty)
	}
}

func TestFindCutPointSplitTurn(t *testing.T) {
	l, _ := newTestLog(t)
	u := appendMsg(t, l, types.Message{Role: "user", Content: "task"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "part one"})
	appendMsg(t, l, types.Message{Role: "tool", Content: "output", ToolCallID: "c"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "part two"})

	path, _ := l.PathTo("")
	cut := FindCutPoint(path, 1)
	if cut.Index != 3 {
		t.Fatalf("cut = %+v", cut)
	}
	if !cut.IsSplitTurn || cut.TurnStartIndex != 0 || cut.TurnStartEntryID != u {
		t.Fatalf("split turn = %+v", cut)
	}
}

func TestFindCutPointCustomMessage(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "a"})
	if _, err := l.Append(&CustomMessageEntry{Content: []ContentBlock{types.TextPart{Text: "custom message"}}}); err != nil {
		t.Fatalf("custom: %v", err)
	}
	appendMsg(t, l, types.Message{Role: "assistant", Content: "answer"})

	path, _ := l.PathTo("")
	cut := FindCutPoint(path, 2)
	if cut.Index != 1 {
		t.Fatalf("cut = %+v", cut)
	}
	if _, ok := path[cut.Index].(*CustomMessageEntry); !ok {
		t.Fatalf("cut entry = %T", path[cut.Index])
	}
	if cut.IsSplitTurn {
		t.Fatalf("custom message must start a turn: %+v", cut)
	}
}

func TestFindCutPointRetainsAdjacentMetadata(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "a"})
	usageID, err := l.Append(&UsageEntry{Kind: "cache_warm", Usage: types.Usage{TotalTokens: 1}})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	appendMsg(t, l, types.Message{Role: "assistant", Content: "answer"})

	path, _ := l.PathTo("")
	cut := FindCutPoint(path, 1)
	if cut.Index != 1 || cut.EntryID != usageID {
		t.Fatalf("cut = %+v, want metadata %s", cut, usageID)
	}
}

func TestBuildCompaction(t *testing.T) {
	cp := BuildCompaction("summary", "e1", 42)
	if cp.EntryType() != EntryCompaction || cp.Summary != "summary" || cp.FirstKeptEntryID != "e1" || cp.TokensBefore != 42 {
		t.Fatalf("compaction = %+v", cp)
	}

	usage := &types.Usage{PromptTokens: 3, TotalTokens: 5}
	cp = BuildCompactionWithOptions(CompactionOptions{
		Summary:          "s",
		FirstKeptEntryID: "e2",
		TokensBefore:     9,
		SystemCheckpoint: &SystemMessage{Role: "system", Content: "prompt"},
		Usage:            usage,
		Details:          json.RawMessage(`{"readFiles":["a"]}`),
	})
	if cp.SystemCheckpoint == nil || cp.SystemCheckpoint.Content != "prompt" {
		t.Fatalf("checkpoint = %+v", cp.SystemCheckpoint)
	}
	if cp.Usage == nil || cp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", cp.Usage)
	}
	if string(cp.Details) != `{"readFiles":["a"]}` {
		t.Fatalf("details = %s", cp.Details)
	}

	cp.EntryBase.ID = "cp-roundtrip"
	raw, err := MarshalEntry(cp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := UnmarshalEntry(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded.(*CompactionEntry), cp) {
		t.Fatalf("decoded = %#v, want %#v", decoded, cp)
	}
}

func TestFormatCompactionInput(t *testing.T) {
	long := strings.Repeat("z", 2500)
	msgs := []types.Message{
		{Role: "user", Content: "U"},
		{Role: "assistant", Content: "A", ToolCalls: []types.ToolCall{
			{ID: "c", Type: "function", Function: types.ToolFunction{Name: "read", Arguments: map[string]any{"p": "x"}}},
		}},
		{Role: "tool", Content: long, ToolCallID: "c"},
		{Role: "system", Content: "S"},
	}
	got := FormatCompactionInput("PREV", msgs)
	for _, want := range []string{
		"<previous_summary>\nPREV\n</previous_summary>",
		"[User]\nU",
		"[Assistant]\nA",
		"[Assistant tool calls]\nread(",
		"[Tool result]",
		"…[truncated]",
		"[System]\nS",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("input missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "z") != maxCompactionToolResultRunes {
		t.Errorf("tool result not truncated: %d z's", strings.Count(got, "z"))
	}
	if strings.Contains(FormatCompactionInput("", msgs), "previous_summary") {
		t.Error("empty previous summary must not emit a previous_summary block")
	}
}

func pathIDs(path []Entry) []string {
	ids := make([]string, len(path))
	for i, e := range path {
		ids[i] = e.Base().ID
	}
	return ids
}
