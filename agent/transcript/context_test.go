package transcript

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func appendMsg(t *testing.T, l *Log, m types.Message) string {
	t.Helper()
	id, err := l.AppendMessage(m)
	if err != nil {
		t.Fatalf("append %s: %v", m.Role, err)
	}
	return id
}

func TestBuildContextAppliesLatestCompaction(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "system", Content: "old system prompt"})
	appendMsg(t, l, types.Message{Role: "user", Content: "summarized user"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "summarized assistant"})
	appendMsg(t, l, types.Message{Role: "tool", Content: "summarized tool", ToolCallID: "c1"})
	keptUser := appendMsg(t, l, types.Message{Role: "user", Content: "kept user"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "kept answer"})
	if _, err := l.Append(&CompactionEntry{
		EntryBase:        EntryBase{ID: "cp1"},
		Summary:          "SUM",
		FirstKeptEntryID: keptUser,
		TokensBefore:     123,
		SystemCheckpoint: &SystemMessage{Role: "system", Content: "CHECKPOINT"},
	}); err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	appendMsg(t, l, types.Message{Role: "user", Content: "after compaction"})

	msgs, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []types.Message{
		{Role: "system", Content: "CHECKPOINT"},
		{Role: "system", Content: "SUM"},
		{Role: "user", Content: "kept user"},
		{Role: "assistant", Content: "kept answer"},
		{Role: "user", Content: "after compaction"},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("context mismatch:\nwant %#v\ngot  %#v", want, msgs)
	}
	if report.CompactionID != "cp1" {
		t.Fatalf("compaction id = %q", report.CompactionID)
	}
	if report.Cut != 0 || report.Tokens == 0 || report.TokensBefore != report.Tokens {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", report.Diagnostics)
	}
}

func TestBuildContextCheckpointAndCustomOptions(t *testing.T) {
	l, _ := newTestLog(t)
	u := appendMsg(t, l, types.Message{Role: "user", Content: "kept"})
	if _, err := l.Append(&CompactionEntry{
		EntryBase:        EntryBase{ID: "cp1"},
		Summary:          "SUM",
		FirstKeptEntryID: u,
		SystemCheckpoint: &SystemMessage{Content: "CHECKPOINT"},
	}); err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	if _, err := l.Append(&CustomMessageEntry{
		CustomType: "ext",
		Content:    []ContentBlock{types.TextPart{Text: "injected"}},
		Display:    true,
	}); err != nil {
		t.Fatalf("append custom: %v", err)
	}

	msgs, _, err := l.BuildContext(context.Background(), BuildOptions{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []types.Message{{Role: "system", Content: "SUM"}, {Role: "user", Content: "kept"}}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("without options = %#v", msgs)
	}

	msgs, _, err = l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want = []types.Message{
		{Role: "system", Content: "CHECKPOINT"},
		{Role: "system", Content: "SUM"},
		{Role: "user", Content: "kept"},
		{Role: "user", Content: "injected"},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("with options = %#v", msgs)
	}
}

func TestBuildContextBranchSummaryAndStateEntries(t *testing.T) {
	l, _ := newTestLog(t)
	u := appendMsg(t, l, types.Message{Role: "user", Content: "hi"})
	if _, err := l.Append(&BranchSummaryEntry{Summary: "abandoned branch", FromID: u}); err != nil {
		t.Fatalf("branch summary: %v", err)
	}
	if _, err := l.Append(&UsageEntry{Kind: "cache_warm", Usage: types.Usage{TotalTokens: 1}}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if _, err := l.Append(&CustomEntry{CustomType: "state"}); err != nil {
		t.Fatalf("custom: %v", err)
	}
	if _, err := l.Append(&LabelEntry{TargetID: u, Label: "x"}); err != nil {
		t.Fatalf("label: %v", err)
	}
	if _, err := l.Append(&SessionInfoEntry{Name: "name"}); err != nil {
		t.Fatalf("info: %v", err)
	}

	msgs, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "abandoned branch"},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("messages = %#v", msgs)
	}
	if report.CompactionID != "" {
		t.Fatalf("compaction id = %q", report.CompactionID)
	}
}

func TestBuildContextFirstKeptMissingIsDiagnosed(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "before"})
	if _, err := l.Append(&CompactionEntry{
		EntryBase:        EntryBase{ID: "cp1"},
		Summary:          "SUM",
		FirstKeptEntryID: "does-not-exist",
	}); err != nil {
		t.Fatalf("append compaction: %v", err)
	}
	appendMsg(t, l, types.Message{Role: "user", Content: "after"})

	msgs, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []types.Message{{Role: "system", Content: "SUM"}, {Role: "user", Content: "after"}}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("messages = %#v", msgs)
	}
	found := false
	for _, d := range report.Diagnostics {
		if strings.Contains(d.Message, "firstKeptEntryId") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v", report.Diagnostics)
	}
}

func TestBuildContextDropsOrphanedToolResult(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "task"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
		{ID: "call-1", Type: "function", Function: types.ToolFunction{Name: "read"}},
	}})
	toolID := appendMsg(t, l, types.Message{Role: "tool", Content: "output", ToolCallID: "call-1"})
	if _, err := l.Append(&CompactionEntry{
		EntryBase:        EntryBase{ID: "cp1"},
		Summary:          "SUM",
		FirstKeptEntryID: toolID, // malformed: cuts the tool pair
	}); err != nil {
		t.Fatalf("append compaction: %v", err)
	}

	msgs, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []types.Message{{Role: "system", Content: "SUM"}}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("messages = %#v", msgs)
	}
	found := false
	for _, d := range report.Diagnostics {
		if strings.Contains(d.Message, "orphaned tool result") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v", report.Diagnostics)
	}
}

func TestBuildContextSettingsAndLeafOverride(t *testing.T) {
	l, _ := newTestLog(t)
	u := appendMsg(t, l, types.Message{Role: "user", Content: "hi"})
	if _, err := l.Append(&ModelChangeEntry{Provider: "p1", ModelID: "m1"}); err != nil {
		t.Fatalf("model: %v", err)
	}
	if _, err := l.Append(&ThinkingLevelChangeEntry{ThinkingLevel: "low"}); err != nil {
		t.Fatalf("thinking: %v", err)
	}
	appendMsg(t, l, types.Message{Role: "assistant", Content: "ok"})
	if _, err := l.Append(&ModelChangeEntry{Provider: "p2", ModelID: "m2"}); err != nil {
		t.Fatalf("model: %v", err)
	}
	if _, err := l.Append(&ThinkingLevelChangeEntry{ThinkingLevel: "high"}); err != nil {
		t.Fatalf("thinking: %v", err)
	}

	msgs, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v", msgs)
	}
	if report.Model == nil || report.Model.Provider != "p2" || report.Model.ModelID != "m2" {
		t.Fatalf("model = %+v", report.Model)
	}
	if report.ThinkingLevel != "high" {
		t.Fatalf("thinking = %q", report.ThinkingLevel)
	}

	// Leaf override stops the replay at the requested entry.
	msgs, _, err = l.BuildContext(context.Background(), BuildOptions{LeafID: u})
	if err != nil {
		t.Fatalf("build leaf: %v", err)
	}
	want := []types.Message{{Role: "user", Content: "hi"}}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("leaf context = %#v", msgs)
	}
}

func TestBuildContextUsageFallsBackToModel(t *testing.T) {
	l, _ := newTestLog(t)
	if _, err := l.Append(&UsageEntry{Kind: "cache_warm", Provider: "p", Model: "m"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	_, report, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if report.Model == nil || report.Model.Provider != "p" || report.Model.ModelID != "m" {
		t.Fatalf("model = %+v", report.Model)
	}
}

func TestBuildContextIsDeterministicAcrossReopen(t *testing.T) {
	l, path := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "one"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "two"})
	appendMsg(t, l, types.Message{Role: "user", Content: "three"})

	first, firstReport, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	second, secondReport, err := l.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstReport, secondReport) {
		t.Fatal("in-memory replay is not deterministic")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ro, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ro.Close()
	third, thirdReport, err := ro.BuildContext(context.Background(), DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build reopened: %v", err)
	}
	if !reflect.DeepEqual(first, third) || !reflect.DeepEqual(firstReport, thirdReport) {
		t.Fatal("replayed context differs after reopen")
	}
}

func testToolPairMessages() []types.Message {
	return []types.Message{
		{Role: "user", Content: "old user"},
		{Role: "assistant", Content: "old answer"},
		{Role: "assistant", Content: "calling", ToolCalls: []types.ToolCall{
			{ID: "call-1", Type: "function", Function: types.ToolFunction{Name: "read", Arguments: map[string]any{"path": "x"}}},
		}},
		{Role: "tool", Content: "tool output", ToolCallID: "call-1", Name: "read"},
		{Role: "user", Content: "recent user"},
		{Role: "assistant", Content: "recent answer"},
	}
}

func TestBuildContextBudgetNeverOrphansToolResult(t *testing.T) {
	l, _ := newTestLog(t)
	for _, m := range testToolPairMessages() {
		appendMsg(t, l, m)
	}
	all, report, err := l.BuildContext(context.Background(), BuildOptions{})
	if err != nil {
		t.Fatalf("build all: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("all = %d", len(all))
	}

	for budget := 1; budget <= report.TokensBefore; budget++ {
		msgs, r, err := l.BuildContext(context.Background(), BuildOptions{MaxTokens: budget})
		if err != nil {
			t.Fatalf("build budget %d: %v", budget, err)
		}
		assertToolResultsHaveCalls(t, budget, msgs)
		if r.Cut < 0 || r.Cut > len(all) {
			t.Fatalf("budget %d: cut = %d", budget, r.Cut)
		}
		if r.Cut > 0 && r.CutEntryID == "" {
			t.Fatalf("budget %d: missing cut entry id", budget)
		}
	}

	// With a budget that only fits the recent tail, the cut lands on the recent
	// user message rather than the tool result.
	recentTokens := types.RoughTokensForMessage(all[4]) + types.RoughTokensForMessage(all[5])
	msgs, r, err := l.BuildContext(context.Background(), BuildOptions{MaxTokens: recentTokens})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if r.Cut != 4 {
		t.Fatalf("cut = %d, want 4", r.Cut)
	}
	if len(msgs) != 2 || msgs[0].Content != "recent user" {
		t.Fatalf("tail = %#v", msgs)
	}
}

func assertToolResultsHaveCalls(t *testing.T, budget int, msgs []types.Message) {
	t.Helper()
	available := map[string]bool{}
	for i, m := range msgs {
		if m.Role == "assistant" {
			for _, call := range m.ToolCalls {
				available[call.ID] = true
			}
		}
		if m.Role == "tool" {
			if i == 0 || !available[m.ToolCallID] {
				t.Fatalf("budget %d: orphaned tool result at index %d (id %q): %#v", budget, i, m.ToolCallID, msgs)
			}
		}
	}
}
