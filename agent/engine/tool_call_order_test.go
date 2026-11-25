package engine

import (
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

type depTool struct {
	*MockTool
	deps []string
}

func (d depTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{Dependencies: d.deps}
}

func toolCall(id, name string) types.ToolCall {
	return types.ToolCall{ID: id, Function: types.ToolFunction{Name: name}}
}

// Claude Code returns tool results in the order the model emitted the
// tool_use blocks. Cortex used to key calls by tool name, which collapsed two
// Read calls in one batch into one; the replacement is index-keyed.
func TestSortToolCallsPreservesOrderAndDuplicateNames(t *testing.T) {
	ae := &AgentEngine{toolsMap: map[string]types.Tool{
		"Read": NewMockTool("Read"),
		"Glob": NewMockTool("Glob"),
	}}
	calls := []types.ToolCall{
		toolCall("a", "Read"),
		toolCall("b", "Read"),
		toolCall("c", "Glob"),
	}
	got, err := ae.sortToolCallsByDependencies(calls)
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d calls, want 3 (duplicate names must be preserved)", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].ID != want {
			t.Fatalf("call[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

// Dependency metadata still orders calls: a tool that depends on a later call
// is moved after its dependency.
func TestSortToolCallsHonorsDependencies(t *testing.T) {
	ae := &AgentEngine{toolsMap: map[string]types.Tool{
		"wait":  depTool{MockTool: NewMockTool("wait"), deps: []string{"spawn"}},
		"spawn": NewMockTool("spawn"),
	}}
	calls := []types.ToolCall{
		toolCall("w", "wait"),
		toolCall("s", "spawn"),
	}
	got, err := ae.sortToolCallsByDependencies(calls)
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if len(got) != 2 || got[0].ID != "s" || got[1].ID != "w" {
		t.Fatalf("order = %v, want spawn before wait", []string{got[0].ID, got[1].ID})
	}
}

// All same-layer calls stay in one layer, so they can run concurrently but
// results remain index-aligned.
func TestGroupToolCallsByLayerPreservesIndices(t *testing.T) {
	ae := &AgentEngine{toolsMap: map[string]types.Tool{
		"Read": NewMockTool("Read"),
	}}
	layers := ae.groupSortedToolCallsByLayer([]types.ToolCall{
		toolCall("a", "Read"),
		toolCall("b", "Read"),
	})
	if len(layers) != 1 || len(layers[0]) != 2 || layers[0][0] != 0 || layers[0][1] != 1 {
		t.Fatalf("layers = %v, want [[0 1]]", layers)
	}
}

type orderMetaTool struct {
	*MockTool
	md types.ToolMetadata
}

func (m orderMetaTool) Metadata() types.ToolMetadata { return m.md }

type dynSafeTool struct {
	*MockTool
}

func (d dynSafeTool) IsConcurrencySafe(input map[string]interface{}) bool {
	return input["read_only"] == true
}

// Read-only tools may run concurrently; mutating/custom tools are barriers.
func TestToolConcurrencySafety(t *testing.T) {
	read := &orderMetaTool{MockTool: NewMockTool("Read"), md: types.ToolMetadata{ConcurrencySafe: true}}
	write := &orderMetaTool{MockTool: NewMockTool("Write"), md: types.ToolMetadata{ConcurrencySafe: false}}
	custom := NewMockTool("Custom")
	if !toolConcurrencySafe(read, nil) {
		t.Fatal("Read should be concurrency-safe")
	}
	if toolConcurrencySafe(write, nil) {
		t.Fatal("Write should be a barrier")
	}
	if toolConcurrencySafe(custom, nil) {
		t.Fatal("custom tools default to sequential")
	}
	dyn := &dynSafeTool{MockTool: NewMockTool("Bash")}
	if !toolConcurrencySafe(dyn, map[string]interface{}{"read_only": true}) {
		t.Fatal("provider should allow read-only invocation")
	}
	if toolConcurrencySafe(dyn, map[string]interface{}{"read_only": false}) {
		t.Fatal("provider should block mutating invocation")
	}
}
