package runtime

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
)

// ToolSearchToolName is the search tool injected into the initial tool list (E2).
const ToolSearchToolName = "tool_search"

// ToolSearchTool 检索所有 Deferred 工具并返回匹配（E2 轻量版）。
// 发现逻辑（把命中的工具注入 ae.tools）由 factory 在构造时经 SetDiscover
// 注入 Execute 闭包完成（机制 A，见 docs/design/tools-codex-eval.md §3.3）。
type ToolSearchTool struct {
	index    *ToolSearchIndex
	discover func(ctx context.Context, name string) error
}

var _ types.Tool = (*ToolSearchTool)(nil)

// NewToolSearchTool creates a tool_search bound to the given index.
func NewToolSearchTool(index *ToolSearchIndex) *ToolSearchTool {
	return &ToolSearchTool{index: index}
}

// SetDiscover wires the discovery callback. Called by dino/factory at session
// construction; nil is a no-op (tool still searches but never injects).
func (t *ToolSearchTool) SetDiscover(fn func(ctx context.Context, name string) error) {
	t.discover = fn
}

func (t *ToolSearchTool) Name() string { return ToolSearchToolName }

//go:embed tool_search.txt
var toolSearchDescription string

func (t *ToolSearchTool) Description() string { return toolSearchDescription }

func (t *ToolSearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query, e.g. 'github' or 'memory'",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Max results (default 8)",
			},
		},
		"required": []string{"query"},
	}
}

// DiscoverResult is the structured payload returned by tool_search. `discovered`
// lists the tool names that became available starting the next turn (pure
// hint under mechanism A — injection already happened inside Execute).
type DiscoverResult struct {
	// Matched tools (name/description/category), sorted by relevance.
	Results []SearchResult `json:"results"`
	// Discovered lists the tool names injected into the next turn's tool list.
	Discovered []string `json:"discovered,omitempty"`
	// LimitReached is true when the session-level discovery cap was hit and no
	// more tools can be injected (R1).
	LimitReached bool `json:"limit_reached,omitempty"`
}

func (t *ToolSearchTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	query, _ := input["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	maxResults := 8
	if mr, ok := input["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}

	results := t.index.Search(query, maxResults)
	// Empty results are a normal outcome, not an error.
	if len(results) == 0 {
		return DiscoverResult{Results: []SearchResult{}}, nil
	}

	var discovered []string
	for _, r := range results {
		discovered = append(discovered, r.Name)
	}

	out := DiscoverResult{
		Results:    results,
		Discovered: discovered,
	}

	// Mechanism A (BLOCKER B1 resolved): inject immediately, inside Execute.
	// Idempotent — discover returns an error for an already-injected tool, which
	// we surface but do not fail the search on. De-duplicate so a tool injected
	// earlier in this same call is not reported twice.
	if t.discover != nil {
		var actuallyDiscovered []string
		seen := make(map[string]bool)
		for _, name := range discovered {
			if seen[name] {
				continue
			}
			seen[name] = true
			if err := t.discover(ctx, name); err != nil {
				// Already injected or cap reached: keep it out of the report.
				continue
			}
			actuallyDiscovered = append(actuallyDiscovered, name)
		}
		out.Discovered = actuallyDiscovered
	}

	return out, nil
}

func (t *ToolSearchTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: ToolSearchToolName,
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}
