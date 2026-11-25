package engine

import (
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func call(id string) types.ToolCall {
	return types.ToolCall{ID: id, Function: types.ToolFunction{Name: "Read"}}
}

func TestRepairToolPairingKeepsValidHistory(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []types.ToolCall{call("a")}},
		{Role: "tool", ToolCallID: "a", Content: "result-a"},
	}
	got := repairToolPairing(msgs)
	if len(got) != 3 || got[2].ToolCallID != "a" {
		t.Fatalf("valid history changed: %#v", got)
	}
}

func TestRepairToolPairingDropsOrphanResults(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", ToolCallID: "ghost", Content: "orphan"},
		{Role: "assistant", Content: "done"},
	}
	got := repairToolPairing(msgs)
	for _, m := range got {
		if m.ToolCallID == "ghost" {
			t.Fatalf("orphan tool result survived: %#v", got)
		}
	}
}

func TestRepairToolPairingSynthesizesMissingResult(t *testing.T) {
	msgs := []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call("a")}},
		{Role: "user", Content: "next"},
	}
	got := repairToolPairing(msgs)
	if len(got) != 3 || got[1].Role != "tool" || got[1].ToolCallID != "a" {
		t.Fatalf("missing result not synthesized: %#v", got)
	}
}

func TestRepairToolPairingOrdersResultsByCall(t *testing.T) {
	msgs := []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call("a"), call("b")}},
		{Role: "tool", ToolCallID: "b", Content: "result-b"},
		{Role: "tool", ToolCallID: "a", Content: "result-a"},
	}
	got := repairToolPairing(msgs)
	if got[1].ToolCallID != "a" || got[2].ToolCallID != "b" {
		t.Fatalf("results not in call order: %#v", got)
	}
}
