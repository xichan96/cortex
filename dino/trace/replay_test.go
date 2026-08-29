package trace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// makeReplayEvents builds a synthetic event stream covering one turn with an
// LLM call and a tool call/result.
func makeReplayEvents() []TraceEvent {
	evts := []TraceEvent{
		{
			SchemaVersion: 1, Seq: 1, WallTimeUnixMS: 1000,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventTurnStart,
			Payload: mustJSON(TurnStartPayload{Input: types.AgentInput{Text: "hi"}, Model: "gpt-4o"}),
		},
		{
			SchemaVersion: 1, Seq: 2, WallTimeUnixMS: 1100,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventLLMCall,
			Iteration: intPtr(0),
			Payload: mustJSON(LLMCallPayload{Messages: []types.Message{{Role: "user", Content: "hi"}}, EstTokensIn: 10}),
		},
		{
			SchemaVersion: 1, Seq: 3, WallTimeUnixMS: 1600,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventLLMCallEnd,
			Iteration: intPtr(0),
			Payload: mustJSON(LLMCallEndPayload{
				Usage:      types.Usage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17},
				DurationMS: 500,
				ToolCalls:  []types.ToolCallRequest{{Tool: "read_file", ToolCallID: "c1"}},
			}),
		},
		{
			SchemaVersion: 1, Seq: 4, WallTimeUnixMS: 1700,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventToolCall,
			Iteration: intPtr(1),
			Payload: mustJSON(ToolCallPayload{
				ToolName: "read_file", ToolCallID: "c1", Input: map[string]any{"path": "/x"},
			}),
		},
		{
			SchemaVersion: 1, Seq: 5, WallTimeUnixMS: 2400,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventToolResult,
			Iteration: intPtr(1),
			Payload: mustJSON(ToolResultPayload{
				ToolName: "read_file", ToolCallID: "c1", Output: "file content", DurationMS: 700,
			}),
		},
		{
			SchemaVersion: 1, Seq: 6, WallTimeUnixMS: 3000,
			TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventTurnEnd,
			Payload: mustJSON(TurnEndPayload{
				Output: "done", Usage: types.Usage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17},
				Iterations: 2, WallMS: 2000,
			}),
		},
	}
	return evts
}

func intPtr(i int) *int { return &i }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestReplayRenderText(t *testing.T) {
	evts := makeReplayEvents()
	text := RenderText(evts)
	for _, want := range []string{"Turn t1", "session s1", "llm_call (iter 0)", "TOOL read_file [c1]", "final: done"} {
		if !strings.Contains(text, want) {
			t.Errorf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestReplayReduceJSON(t *testing.T) {
	evts := makeReplayEvents()
	turns := Reduce(evts)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	tr := turns[0]
	if tr.TraceID != "t1" || tr.Input != "hi" || tr.FinalOutput != "done" {
		t.Errorf("turn basics: %+v", tr)
	}
	if tr.Usage.PromptTokens != 12 || tr.Usage.CompletionTokens != 5 {
		t.Errorf("usage: %+v", tr.Usage)
	}
	if tr.WallMS != 2000 {
		t.Errorf("wall_ms = %d, want 2000", tr.WallMS)
	}
	if len(tr.Iterations) != 2 {
		t.Errorf("iterations = %d, want 2", len(tr.Iterations))
	}
	// Iteration 0 has the LLM call; iteration 1 has the tool call.
	foundTool := false
	for _, iter := range tr.Iterations {
		for _, tc := range iter.ToolCalls {
			if tc.ToolName == "read_file" && tc.ToolCallID == "c1" {
				if tc.Output != "file content" || tc.DurationMS != 700 {
					t.Errorf("tool call folded wrong: %+v", tc)
				}
				foundTool = true
			}
		}
	}
	if !foundTool {
		t.Errorf("tool call not paired")
	}
}

func TestReplaySubagentTree(t *testing.T) {
	evts := []TraceEvent{
		{SchemaVersion: 1, Seq: 1, WallTimeUnixMS: 1000, TraceID: "root", SessionID: "s1", TurnID: 1, Type: EventTurnStart, Payload: mustJSON(TurnStartPayload{Input: types.AgentInput{Text: "root task"}})},
		{SchemaVersion: 1, Seq: 2, WallTimeUnixMS: 2000, TraceID: "sub1", SessionID: "s1", TurnID: 1, Type: EventTurnStart, ThreadID: "/root/task_1", ParentTraceID: "root", Payload: mustJSON(TurnStartPayload{Input: types.AgentInput{Text: "sub task"}})},
		{SchemaVersion: 1, Seq: 3, WallTimeUnixMS: 3000, TraceID: "sub1", SessionID: "s1", TurnID: 1, Type: EventTurnEnd, ThreadID: "/root/task_1", ParentTraceID: "root", Payload: mustJSON(TurnEndPayload{Output: "sub done", Iterations: 1, WallMS: 1000})},
		{SchemaVersion: 1, Seq: 4, WallTimeUnixMS: 4000, TraceID: "root", SessionID: "s1", TurnID: 1, Type: EventTurnEnd, Payload: mustJSON(TurnEndPayload{Output: "root done", Iterations: 1, WallMS: 3000})},
	}
	turns := Reduce(evts)
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(turns))
	}
	// Find the subagent turn.
	var sub *ReducedTurn
	for _, tr := range turns {
		if tr.TraceID == "sub1" {
			sub = tr
		}
	}
	if sub == nil {
		t.Fatal("subagent turn missing")
	}
	if sub.ThreadID != "/root/task_1" {
		t.Errorf("thread_id = %q", sub.ThreadID)
	}
	if sub.ParentTraceID != "root" {
		t.Errorf("parent_trace_id = %q, want root", sub.ParentTraceID)
	}
	if sub.FinalOutput != "sub done" {
		t.Errorf("sub output = %q", sub.FinalOutput)
	}
}

func TestReplayDanglingToolCall(t *testing.T) {
	evts := []TraceEvent{
		{SchemaVersion: 1, Seq: 1, WallTimeUnixMS: 1000, TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventToolCall, Payload: mustJSON(ToolCallPayload{ToolName: "bash", ToolCallID: "cX", Input: map[string]any{"cmd": "ls"}})},
		{SchemaVersion: 1, Seq: 2, WallTimeUnixMS: 1000, TraceID: "t1", SessionID: "s1", TurnID: 1, Type: EventTurnEnd, Payload: mustJSON(TurnEndPayload{Iterations: 1, WallMS: 0})},
	}
	turns := Reduce(evts)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	found := false
	for _, iter := range turns[0].Iterations {
		for _, tc := range iter.ToolCalls {
			if tc.ToolCallID == "cX" && tc.Dangling {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("dangling tool call not marked")
	}
}
