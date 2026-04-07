package search

import (
	"context"
	"testing"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/stores"
	"github.com/xichan96/cortex/pkg/memkit/typescore"
)

type mockLLMProvider struct {
	response string
	err      error
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []typescore.Message) (typescore.Message, error) {
	return typescore.Message{Content: m.response}, m.err
}

func (m *mockLLMProvider) ChatWithTools(ctx context.Context, messages []typescore.Message, tools []typescore.Tool) (typescore.Message, error) {
	return typescore.Message{Content: m.response}, m.err
}

func TestIndexBuilder(t *testing.T) {
	t.Run("NewIndexBuilder", func(t *testing.T) {
		builder := NewIndexBuilder(nil, nil)
		if builder == nil {
			t.Error("expected non-nil builder")
		}
		if builder.config == nil {
			t.Error("expected non-nil config")
		}
	})

	t.Run("NewIndexBuilderWithStore", func(t *testing.T) {
		store := stores.NewInMemoryIndexStore()
		builder := NewIndexBuilderWithStore(nil, nil, store)
		if builder == nil {
			t.Error("expected non-nil builder")
		}
		if builder.store != store {
			t.Error("expected store to be set")
		}
	})

	t.Run("DefaultIndexConfig", func(t *testing.T) {
		cfg := stores.DefaultIndexConfig()
		if cfg.MaxNodesPerTree != 1000 {
			t.Errorf("expected MaxNodesPerTree 1000, got %d", cfg.MaxNodesPerTree)
		}
		if cfg.EnableSummary != true {
			t.Error("expected EnableSummary true")
		}
		if cfg.SummaryThreshold != 200 {
			t.Errorf("expected SummaryThreshold 200, got %d", cfg.SummaryThreshold)
		}
		if cfg.VerificationEnabled != true {
			t.Error("expected VerificationEnabled true")
		}
	})

	t.Run("BuildFromMarkdown", func(t *testing.T) {
		store := stores.NewInMemoryIndexStore()
		builder := NewIndexBuilderWithStore(nil, &stores.IndexConfig{
			EnableSummary:       false,
			VerificationEnabled: false,
		}, store)

		content := `# Title
## Section 1
Content of section 1
## Section 2
Content of section 2`

		tree, err := builder.BuildFromMarkdown(context.Background(), "user1", "doc1", "Test Doc", content)
		if err != nil {
			t.Fatalf("BuildFromMarkdown failed: %v", err)
		}
		if tree == nil {
			t.Fatal("expected non-nil tree")
		}
		if len(tree.Nodes) == 0 {
			t.Error("expected nodes in tree")
		}
	})

	t.Run("BuildFromMarkdown with thinning", func(t *testing.T) {
		store := stores.NewInMemoryIndexStore()
		builder := NewIndexBuilderWithStore(nil, &stores.IndexConfig{
			EnableSummary:  false,
			EnableThinning: true,
			MinNodeTokens:  10,
		}, store)

		content := `# Title
## Section 1
Content here
## Section 2
More content here`

		tree, err := builder.BuildFromMarkdown(context.Background(), "user1", "doc2", "Test Doc", content)
		if err != nil {
			t.Fatalf("BuildFromMarkdown failed: %v", err)
		}
		if tree == nil {
			t.Fatal("expected non-nil tree")
		}
	})

	t.Run("indexNodeRoots", func(t *testing.T) {
		nodes := []*stores.IndexNode{
			{ID: "1", Title: "Root", Level: 1, ParentID: ""},
			{ID: "2", Title: "Child", Level: 2, ParentID: "1"},
		}

		roots := indexNodeRoots(nodes)
		if len(roots) != 1 {
			t.Errorf("expected 1 root, got %d", len(roots))
		}
		if roots[0].ID != "1" {
			t.Errorf("expected root ID 1, got %s", roots[0].ID)
		}
	})

	t.Run("flattenIndexPreorder", func(t *testing.T) {
		roots := []*stores.IndexNode{
			{ID: "1", Title: "Root", Level: 1, Nodes: []*stores.IndexNode{
				{ID: "2", Title: "Child", Level: 2},
			}},
		}

		flat := flattenIndexPreorder(roots)
		if len(flat) != 2 {
			t.Errorf("expected 2 nodes, got %d", len(flat))
		}
		if flat[0].ID != "1" {
			t.Errorf("expected first node ID 1, got %s", flat[0].ID)
		}
	})

	t.Run("parseTags valid JSON", func(t *testing.T) {
		tags := parseTags(`["tag1", "tag2", "tag3"]`)
		if len(tags) != 3 {
			t.Errorf("expected 3 tags, got %d", len(tags))
		}
	})

	t.Run("parseTags with backticks", func(t *testing.T) {
		tags := parseTags("```json\n[\"tag1\", \"tag2\"]\n```")
		if len(tags) != 2 {
			t.Errorf("expected 2 tags, got %d", len(tags))
		}
	})

	t.Run("parseTags invalid", func(t *testing.T) {
		tags := parseTags("not json")
		if len(tags) != 0 {
			t.Error("expected empty tags for invalid input")
		}
	})

	t.Run("parseTags empty", func(t *testing.T) {
		tags := parseTags("[]")
		if len(tags) != 0 {
			t.Error("expected empty tags for empty array")
		}
	})

	t.Run("parseVerification valid", func(t *testing.T) {
		result := parseVerification(`{"valid": true, "reason": "ok"}`)
		if result["valid"] != "true" {
			t.Errorf("expected valid=true, got %s", result["valid"])
		}
	})

	t.Run("parseVerification with backticks", func(t *testing.T) {
		result := parseVerification("```json\n{\"valid\": false}\n```")
		if result["valid"] != "false" {
			t.Errorf("expected valid=false, got %s", result["valid"])
		}
	})

	t.Run("parseVerification string contains", func(t *testing.T) {
		result := parseVerification(`"valid": true`)
		if result["valid"] != "true" {
			t.Errorf("expected valid=true, got %s", result["valid"])
		}
	})
}

func TestChunkBuilder(t *testing.T) {
	t.Run("NewChunkBuilder", func(t *testing.T) {
		builder := NewChunkBuilder(100, 20)
		if builder.maxTokens != 100 {
			t.Errorf("expected maxTokens 100, got %d", builder.maxTokens)
		}
		if builder.overlapTokens != 20 {
			t.Errorf("expected overlapTokens 20, got %d", builder.overlapTokens)
		}
		if builder.separator != "\n\n" {
			t.Errorf("expected separator \\n\\n, got %s", builder.separator)
		}
	})

	t.Run("ChunkByTokens short text", func(t *testing.T) {
		builder := NewChunkBuilder(1000, 0)
		chunks := builder.ChunkByTokens("short text", nil)
		if len(chunks) != 1 {
			t.Errorf("expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0] != "short text" {
			t.Error("expected chunk to be original text")
		}
	})

	t.Run("ChunkByTokens long text", func(t *testing.T) {
		builder := NewChunkBuilder(50, 0)
		text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
		chunks := builder.ChunkByTokens(text, nil)
		if len(chunks) > 1 {
			t.Logf("got %d chunks", len(chunks))
		}
	})

	t.Run("ChunkByTokens with overlap", func(t *testing.T) {
		builder := NewChunkBuilder(30, 10)
		text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
		chunks := builder.ChunkByTokens(text, nil)
		if len(chunks) >= 2 {
			t.Logf("got %d chunks with overlap", len(chunks))
		}
	})

	t.Run("collectOverlapLines", func(t *testing.T) {
		builder := NewChunkBuilder(100, 15)
		chunk := []string{"line1", "line2", "line3", "line4", "line5"}
		overlap := builder.collectOverlapLines(chunk)
		if len(overlap) > len(chunk) {
			t.Error("overlap should not exceed chunk length")
		}
	})

	t.Run("collectOverlapLines no overlap", func(t *testing.T) {
		builder := NewChunkBuilder(100, 0)
		chunk := []string{"line1", "line2"}
		overlap := builder.collectOverlapLines(chunk)
		if len(overlap) != 0 {
			t.Error("expected no overlap when overlapTokens is 0")
		}
	})
}

func TestTreeSearchFunctions(t *testing.T) {
	t.Run("indexNodeToTreeSearchNode", func(t *testing.T) {
		node := &stores.IndexNode{
			ID:            "node1",
			Title:         "Test Node",
			Level:         1,
			Summary:       "Summary",
			PrefixSummary: "Prefix",
			Nodes:         []*stores.IndexNode{},
		}

		llmNode := indexNodeToTreeSearchNode(node)
		if llmNode.ID != "node1" {
			t.Errorf("expected ID node1, got %s", llmNode.ID)
		}
		if llmNode.Summary != "Summary" {
			t.Errorf("expected Summary, got %s", llmNode.Summary)
		}
	})

	t.Run("indexNodeToTreeSearchNode uses PrefixSummary", func(t *testing.T) {
		node := &stores.IndexNode{
			ID:            "node1",
			Title:         "Test",
			Level:         1,
			PrefixSummary: "Prefix Summary",
			Nodes:         []*stores.IndexNode{},
		}

		llmNode := indexNodeToTreeSearchNode(node)
		if llmNode.Summary != "Prefix Summary" {
			t.Errorf("expected Prefix Summary, got %s", llmNode.Summary)
		}
	})

	t.Run("indexTreeRootNodes", func(t *testing.T) {
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, ParentID: "", CreatedAt: now, UpdatedAt: now},
				"ch1":   {ID: "ch1", Title: "Child", Level: 2, ParentID: "root1", CreatedAt: now, UpdatedAt: now},
			},
		}

		roots := indexTreeRootNodes(tree)
		if len(roots) != 1 {
			t.Errorf("expected 1 root, got %d", len(roots))
		}
		if roots[0].ID != "root1" {
			t.Errorf("expected root1, got %s", roots[0].ID)
		}
	})

	t.Run("indexTreeRootNodes nil tree", func(t *testing.T) {
		roots := indexTreeRootNodes(nil)
		if roots != nil {
			t.Error("expected nil for nil tree")
		}
	})

	t.Run("TreeSearchIndexTree no LLM", func(t *testing.T) {
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, CreatedAt: now, UpdatedAt: now},
			},
		}

		_, err := TreeSearchIndexTree(context.Background(), nil, tree, "query", "")
		if err == nil {
			t.Error("expected error for nil LLM")
		}
	})

	t.Run("TreeSearchIndexTree empty query", func(t *testing.T) {
		llm := &mockLLMProvider{response: "{}"}
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, CreatedAt: now, UpdatedAt: now},
			},
		}

		_, err := TreeSearchIndexTree(context.Background(), llm, tree, "", "")
		if err == nil {
			t.Error("expected error for empty query")
		}
	})

	t.Run("TreeSearchIndexTree with LLM response", func(t *testing.T) {
		llm := &mockLLMProvider{response: `{"thinking":"found it","node_list":["root1"]}`}
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, CreatedAt: now, UpdatedAt: now},
			},
		}

		result, err := TreeSearchIndexTree(context.Background(), llm, tree, "query", "")
		if err != nil {
			t.Fatalf("TreeSearchIndexTree failed: %v", err)
		}
		if len(result.NodeIDs) != 1 {
			t.Errorf("expected 1 node ID, got %d", len(result.NodeIDs))
		}
	})

	t.Run("TreeSearchIndexTree empty query", func(t *testing.T) {
		llm := &mockLLMProvider{response: "{}"}
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, CreatedAt: now, UpdatedAt: now},
			},
		}

		_, err := TreeSearchIndexTree(context.Background(), llm, tree, "", "")
		if err == nil {
			t.Error("expected error for empty query")
		}
	})

	t.Run("TreeSearchIndexTree with LLM response", func(t *testing.T) {
		llm := &mockLLMProvider{response: `{"thinking":"found it","node_list":["root1"]}`}
		now := time.Now()
		tree := &stores.IndexTree{
			RootID:   "root1",
			UserID:   "user1",
			SourceID: "doc1",
			Title:    "Test",
			Nodes: map[string]*stores.IndexNode{
				"root1": {ID: "root1", Title: "Root", Level: 1, CreatedAt: now, UpdatedAt: now},
			},
		}

		result, err := TreeSearchIndexTree(context.Background(), llm, tree, "query", "")
		if err != nil {
			t.Fatalf("TreeSearchIndexTree failed: %v", err)
		}
		if len(result.NodeIDs) != 1 {
			t.Errorf("expected 1 node ID, got %d", len(result.NodeIDs))
		}
	})

	t.Run("parseTreeSearchJSON", func(t *testing.T) {
		result, err := parseTreeSearchJSON(`{"thinking":"test","node_list":["id1","id2"]}`)
		if err != nil {
			t.Fatalf("parseTreeSearchJSON failed: %v", err)
		}
		if result.Thinking != "test" {
			t.Errorf("expected thinking test, got %s", result.Thinking)
		}
		if len(result.NodeIDs) != 2 {
			t.Errorf("expected 2 node IDs, got %d", len(result.NodeIDs))
		}
	})

	t.Run("parseTreeSearchJSON fallback", func(t *testing.T) {
		result, err := parseTreeSearchJSON(`thinking: test node_list: ["id1"]`)
		if err != nil {
			t.Fatalf("parseTreeSearchJSON failed: %v", err)
		}
		if result.Thinking == "" {
			t.Logf("got thinking: %s", result.Thinking)
		}
	})
}
