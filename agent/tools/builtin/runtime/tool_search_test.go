package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

type searchableMockTool struct {
	name     string
	desc     string
	keywords []string
	category string
}

func (t *searchableMockTool) Name() string                    { return t.name }
func (t *searchableMockTool) Description() string             { return t.desc }
func (t *searchableMockTool) Schema() map[string]interface{}  { return map[string]interface{}{} }
func (t *searchableMockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (t *searchableMockTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: t.name, ToolType: "mock", Exposure: types.ExposureDeferred}
}
func (t *searchableMockTool) SearchInfo() SearchInfo {
	return SearchInfo{Keywords: t.keywords, Category: t.category}
}

func makeSearchableTools() []types.Tool {
	return []types.Tool{
		&searchableMockTool{name: "github_list_repos", desc: "list repositories from github", category: "mcp:github"},
		&searchableMockTool{name: "search_knowledge", desc: "search the long-term memory store", keywords: []string{"memory", "recall", "knowledge"}, category: "memory"},
		&searchableMockTool{name: "deploy_service", desc: "deploy a service to the cluster", category: "mcp:ops"},
	}
}

func TestToolSearchIndex_NameHit(t *testing.T) {
	ix := NewToolSearchIndex(makeSearchableTools())
	results := ix.Search("github", 8)
	if len(results) == 0 || results[0].Name != "github_list_repos" {
		t.Fatalf("query 'github': top hit must be github_list_repos, got %+v", results)
	}
	if results[0].Category != "mcp:github" {
		t.Fatalf("category must be carried, got %q", results[0].Category)
	}
}

func TestToolSearchIndex_KeywordHit(t *testing.T) {
	ix := NewToolSearchIndex(makeSearchableTools())
	results := ix.Search("recall", 8)
	found := false
	for _, r := range results {
		if r.Name == "search_knowledge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("query 'recall' must hit search_knowledge via keywords, got %+v", results)
	}
}

func TestToolSearchIndex_DescriptionHit(t *testing.T) {
	ix := NewToolSearchIndex(makeSearchableTools())
	results := ix.Search("cluster", 8)
	if len(results) == 0 || results[0].Name != "deploy_service" {
		t.Fatalf("query 'cluster': top hit must be deploy_service, got %+v", results)
	}
}

func TestToolSearchIndex_NoResult(t *testing.T) {
	ix := NewToolSearchIndex(makeSearchableTools())
	if got := ix.Search("zzz-nonexistent", 8); len(got) != 0 {
		t.Fatalf("no-match query must return empty slice, got %+v", got)
	}
}

func TestToolSearchIndex_EmptyIndex(t *testing.T) {
	ix := NewToolSearchIndex(nil)
	if got := ix.Search("anything", 8); len(got) != 0 {
		t.Fatalf("empty index must return empty slice, got %+v", got)
	}
	// nil index must not panic.
	var nilIx *ToolSearchIndex
	if got := nilIx.Search("x", 8); got != nil {
		t.Fatalf("nil index must return nil, got %+v", got)
	}
}

func TestToolSearchIndex_TokenizeCamelCase(t *testing.T) {
	// Underscore names split on '_' and keep the whole word.
	got := tokenize("github_list_repos")
	want := []string{"github", "list", "repos"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("underscore tokenize: got %v, want %v", got, want)
	}
	// camelCase is segmented into subwords (best effort).
	got = tokenize("searchKnowledge")
	want = []string{"search", "knowledge"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("camelCase tokenize: got %v, want %v", got, want)
	}
}

func TestToolSearchTool_ExecuteReturnsEmptyOnNoMatch(t *testing.T) {
	tool := NewToolSearchTool(NewToolSearchIndex(makeSearchableTools()))
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "zzz"})
	if err != nil {
		t.Fatalf("no-match must not error: %v", err)
	}
	dr, ok := res.(DiscoverResult)
	if !ok {
		t.Fatalf("expected DiscoverResult, got %T", res)
	}
	if dr.Results == nil || len(dr.Results) != 0 {
		t.Fatalf("no-match must return empty results slice, got %+v", dr.Results)
	}
}

func TestToolSearchTool_DiscoverInjectsAndDedupes(t *testing.T) {
	tool := NewToolSearchTool(NewToolSearchIndex(makeSearchableTools()))

	var injected []string
	tool.SetDiscover(func(ctx context.Context, name string) error {
		for _, existing := range injected {
			if existing == name {
				return errAlreadyDiscovered
			}
		}
		injected = append(injected, name)
		return nil
	})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "github deploy"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	dr := res.(DiscoverResult)
	if len(dr.Discovered) != 2 {
		t.Fatalf("want 2 discovered tools, got %v", dr.Discovered)
	}
	for _, name := range dr.Discovered {
		if !containsName(injected, name) {
			t.Fatalf("discovered %s not injected", name)
		}
	}

	// Second call: already-injected tools must not appear in Discovered (idempotent).
	res2, _ := tool.Execute(context.Background(), map[string]interface{}{"query": "github deploy"})
	dr2 := res2.(DiscoverResult)
	if len(dr2.Discovered) != 0 {
		t.Fatalf("re-discovering injected tools must be idempotent, got %v", dr2.Discovered)
	}
}

func containsName(xs []string, name string) bool {
	for _, x := range xs {
		if x == name {
			return true
		}
	}
	return false
}

// errAlreadyDiscovered is the sentinel discover returns for an already-injected
// tool (see factory.discoverTool).
var errAlreadyDiscovered = errString("tool already discovered")

type errString string

func (e errString) Error() string { return string(e) }

// Ensure the tool_search description mentions search (smoke that embed works).
func TestToolSearchDescriptionNonEmpty(t *testing.T) {
	if strings.TrimSpace(toolSearchDescription) == "" {
		t.Fatal("tool_search description must not be empty")
	}
}
