package memkit

import (
	"context"
	"strings"
	"testing"

	"github.com/xichan96/cortex/pkg/memkit/search"
	"github.com/xichan96/cortex/pkg/memkit/stores"
)

func TestInMemoryPreferenceStore(t *testing.T) {
	store := stores.NewInMemoryPreferenceStore()
	ctx := context.Background()

	t.Run("Set and Get", func(t *testing.T) {
		pref := Preference{
			UserID:   "user1",
			Category: "test",
			Key:      "name",
			Value:    "Alice",
		}
		if err := store.Set(ctx, pref); err != nil {
			t.Fatalf("Set failed: %v", err)
		}

		got, err := store.Get(ctx, "user1", "test", "name")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got == nil || got.Value != "Alice" {
			t.Errorf("expected Alice, got %v", got)
		}
	})

	t.Run("GetByUser", func(t *testing.T) {
		prefs, err := store.GetByUser(ctx, "user1")
		if err != nil {
			t.Fatalf("GetByUser failed: %v", err)
		}
		if len(prefs) == 0 {
			t.Error("expected at least 1 preference")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		if err := store.Delete(ctx, "user1", "test", "name"); err != nil {
			t.Fatalf("Delete failed: %v", err)
		}
		got, _ := store.Get(ctx, "user1", "test", "name")
		if got != nil {
			t.Error("expected nil after delete")
		}
	})

	t.Run("Clear", func(t *testing.T) {
		store.Set(ctx, Preference{UserID: "user2", Category: "a", Key: "k", Value: "v"})
		if err := store.Clear(ctx, "user2"); err != nil {
			t.Fatalf("Clear failed: %v", err)
		}
		prefs, _ := store.GetByUser(ctx, "user2")
		if len(prefs) != 0 {
			t.Error("expected 0 preferences after clear")
		}
	})
}

func TestInMemoryKnowledgeStore(t *testing.T) {
	store := stores.NewInMemoryKnowledgeStore()
	ctx := context.Background()

	t.Run("Add and Search", func(t *testing.T) {
		entry := KnowledgeEntry{
			UserID:   "user1",
			Category: "docs",
			Content:  "This is a test document about Go programming",
			Tags:     []string{"go", "programming"},
		}
		if err := store.Add(ctx, entry); err != nil {
			t.Fatalf("Add failed: %v", err)
		}

		result, err := store.Search(ctx, "user1", &SearchOptions{Query: "Go"})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Items) == 0 {
			t.Error("expected at least 1 search result")
		}
	})

	t.Run("Search by Tags", func(t *testing.T) {
		entries, err := store.GetByTags(ctx, "user1", []string{"go"})
		if err != nil {
			t.Fatalf("GetByTags failed: %v", err)
		}
		if len(entries) == 0 {
			t.Error("expected entries with tag 'go'")
		}
	})

	t.Run("Search by Category", func(t *testing.T) {
		entries, err := store.GetByCategory(ctx, "user1", "docs")
		if err != nil {
			t.Fatalf("GetByCategory failed: %v", err)
		}
		if len(entries) == 0 {
			t.Error("expected entries in 'docs' category")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		entries, _ := store.GetByTags(ctx, "user1", []string{"go"})
		if len(entries) > 0 {
			store.Delete(ctx, entries[0].ID)
		}
		entries, _ = store.GetByTags(ctx, "user1", []string{"go"})
		if len(entries) != 0 {
			t.Error("expected 0 entries after delete")
		}
	})

	t.Run("Stats", func(t *testing.T) {
		store.Add(ctx, KnowledgeEntry{UserID: "user3", Content: "test"})
		count, err := store.GetStats(ctx, "user3")
		if err != nil {
			t.Fatalf("GetStats failed: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1, got %d", count)
		}
	})
}

func TestInMemoryIndexStore(t *testing.T) {
	store := stores.NewInMemoryIndexStore()
	ctx := context.Background()

	t.Run("Create and Get Index", func(t *testing.T) {
		nodes := []*IndexNode{
			{ID: "n1", Title: "Introduction", Level: 1, StartLine: 1, EndLine: 10},
			{ID: "n2", Title: "Getting Started", Level: 2, StartLine: 11, EndLine: 50, ParentID: "n1"},
			{ID: "n3", Title: "Installation", Level: 2, StartLine: 51, EndLine: 100, ParentID: "n1"},
			{ID: "n4", Title: "Configuration", Level: 1, StartLine: 101, EndLine: 200},
		}

		tree, err := store.CreateIndex(ctx, "user1", "doc1", "My Document", nodes)
		if err != nil {
			t.Fatalf("CreateIndex failed: %v", err)
		}
		if tree == nil {
			t.Fatal("expected tree to be non-nil")
		}

		got, err := store.GetIndex(ctx, "user1", "doc1")
		if err != nil {
			t.Fatalf("GetIndex failed: %v", err)
		}
		if got == nil || len(got.Nodes) != 4 {
			t.Errorf("expected 4 nodes, got %v", got)
		}
	})

	t.Run("Search Index", func(t *testing.T) {
		result, err := store.SearchIndex(ctx, "user1", "Getting", 10)
		if err != nil {
			t.Fatalf("SearchIndex failed: %v", err)
		}
		if len(result.Nodes) == 0 {
			t.Error("expected at least 1 search result")
		}
	})

	t.Run("Add Node", func(t *testing.T) {
		newNode := &IndexNode{
			Title: "Advanced Topics",
			Level: 1,
		}
		if err := store.AddNode(ctx, "user1", "doc1", newNode, ""); err != nil {
			t.Fatalf("AddNode failed: %v", err)
		}

		tree, _ := store.GetIndex(ctx, "user1", "doc1")
		if len(tree.Nodes) != 5 {
			t.Errorf("expected 5 nodes, got %d", len(tree.Nodes))
		}
	})

	t.Run("Update Node", func(t *testing.T) {
		tree, _ := store.GetIndex(ctx, "user1", "doc1")
		for _, n := range tree.Nodes {
			if n.Title == "Introduction" {
				n.Summary = "An intro to the document"
				if err := store.UpdateNode(ctx, "user1", "doc1", n); err != nil {
					t.Fatalf("UpdateNode failed: %v", err)
				}
				break
			}
		}

		tree, _ = store.GetIndex(ctx, "user1", "doc1")
		for _, n := range tree.Nodes {
			if n.Title == "Introduction" {
				if n.Summary != "An intro to the document" {
					t.Error("expected summary to be updated")
				}
				break
			}
		}
	})

	t.Run("Remove Node", func(t *testing.T) {
		tree, _ := store.GetIndex(ctx, "user1", "doc1")
		var nodeID string
		for _, n := range tree.Nodes {
			if n.Title == "Advanced Topics" {
				nodeID = n.ID
				break
			}
		}
		if nodeID != "" {
			store.RemoveNode(ctx, "user1", "doc1", nodeID)
		}

		tree, _ = store.GetIndex(ctx, "user1", "doc1")
		if len(tree.Nodes) != 4 {
			t.Errorf("expected 4 nodes after removal, got %d", len(tree.Nodes))
		}
	})

	t.Run("Delete Index", func(t *testing.T) {
		if err := store.DeleteIndex(ctx, "user1", "doc1"); err != nil {
			t.Fatalf("DeleteIndex failed: %v", err)
		}
		tree, _ := store.GetIndex(ctx, "user1", "doc1")
		if tree != nil {
			t.Error("expected nil after delete")
		}
	})

	t.Run("Get All Indexes", func(t *testing.T) {
		store.CreateIndex(ctx, "user2", "doc1", "Doc 1", []*IndexNode{{Title: "A", Level: 1}})
		store.CreateIndex(ctx, "user2", "doc2", "Doc 2", []*IndexNode{{Title: "B", Level: 1}})

		indexes, err := store.GetAllIndexes(ctx, "user2")
		if err != nil {
			t.Fatalf("GetAllIndexes failed: %v", err)
		}
		if len(indexes) != 2 {
			t.Errorf("expected 2 indexes, got %d", len(indexes))
		}
	})
}

func TestMarkdownParser(t *testing.T) {
	parser := stores.NewMarkdownParser()

	t.Run("Parse Headers", func(t *testing.T) {
		content := `# Title
## Section 1
### Subsection 1.1
## Section 2
### Subsection 2.1
#### Deep Subsection`

		nodes := parser.Parse(content)
		if len(nodes) != 6 {
			t.Errorf("expected 6 nodes, got %d", len(nodes))
		}

		if nodes[0].Level != 1 || nodes[0].Title != "Title" {
			t.Errorf("unexpected first node: level=%d, title=%s", nodes[0].Level, nodes[0].Title)
		}

		if nodes[2].Level != 3 || nodes[2].Title != "Subsection 1.1" {
			t.Errorf("unexpected third node: level=%d, title=%s", nodes[2].Level, nodes[2].Title)
		}
	})

	t.Run("Parent Child Relations", func(t *testing.T) {
		content := `# Parent
## Child 1
## Child 2
### Grandchild`

		nodes := parser.Parse(content)
		if len(nodes) != 4 {
			t.Fatalf("expected 4 nodes, got %d", len(nodes))
		}

		if nodes[1].ParentID != nodes[0].ID {
			t.Errorf("Child 1 should have ParentID = Parent ID")
		}
		if nodes[3].ParentID != nodes[2].ID {
			t.Errorf("Grandchild should have ParentID = Child 2 ID")
		}
	})

	t.Run("Extract Content", func(t *testing.T) {
		content := `# Title
Some content here.
## Section
More content.
### SubSection
Even more.`

		nodes := parser.Parse(content)
		if len(nodes) >= 3 {
			extracted := parser.ExtractNodeContent(content, nodes[0], nodes[1])
			if extracted == "" {
				t.Error("expected non-empty content")
			}
		}
	})
}

func TestChunkBuilder(t *testing.T) {
	builder := search.NewChunkBuilder(50, 10)

	t.Run("Chunk Text", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 100; i++ {
			sb.WriteString("This is line ")
			sb.WriteString(string(rune('0' + i%10)))
			sb.WriteString(".\n")
		}
		text := sb.String()

		chunks := builder.ChunkByTokens(text, nil)
		if len(chunks) <= 1 {
			t.Errorf("expected multiple chunks, got %d", len(chunks))
		}
	})

	t.Run("Single Chunk", func(t *testing.T) {
		text := "Short text"
		chunks := builder.ChunkByTokens(text, nil)
		if len(chunks) != 1 {
			t.Errorf("expected 1 chunk, got %d", len(chunks))
		}
	})
}

func TestManager(t *testing.T) {
	manager := NewManager(nil)
	ctx := context.Background()

	t.Run("Preference Operations", func(t *testing.T) {
		if err := manager.SetUserPreference(ctx, "user1", "ui", "theme", "dark"); err != nil {
			t.Fatalf("SetUserPreference failed: %v", err)
		}

		val, err := manager.GetUserPreference(ctx, "user1", "ui", "theme")
		if err != nil {
			t.Fatalf("GetUserPreference failed: %v", err)
		}
		if val != "dark" {
			t.Errorf("expected 'dark', got '%s'", val)
		}

		prefs, err := manager.GetUserPreferences(ctx, "user1")
		if err != nil {
			t.Fatalf("GetUserPreferences failed: %v", err)
		}
		if prefs["ui.theme"] != "dark" {
			t.Error("expected prefs[ui.theme] = dark")
		}
	})

	t.Run("Knowledge Operations", func(t *testing.T) {
		if err := manager.AddKnowledge(ctx, "user1", "Go is a programming language", "go", "programming"); err != nil {
			t.Fatalf("AddKnowledge failed: %v", err)
		}

		results, err := manager.SearchKnowledge(ctx, "user1", "programming", 10)
		if err != nil {
			t.Fatalf("SearchKnowledge failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected search results")
		}
	})

	t.Run("Index Operations", func(t *testing.T) {
		content := `# Document
## Chapter 1
Content of chapter 1
## Chapter 2
Content of chapter 2`

		tree, err := manager.BuildIndex(ctx, "user1", "doc1", "Test Doc", content)
		if err != nil {
			t.Fatalf("BuildIndex failed: %v", err)
		}
		if tree == nil || len(tree.Nodes) == 0 {
			t.Error("expected nodes in tree")
		}

		result, err := manager.SearchIndexes(ctx, "user1", "Chapter", 10)
		if err != nil {
			t.Fatalf("SearchIndexes failed: %v", err)
		}
		if len(result.Nodes) < 2 {
			t.Error("expected at least 2 nodes matching 'Chapter'")
		}
	})

	t.Run("Stats", func(t *testing.T) {
		stats, err := manager.GetStats(ctx, "user1")
		if err != nil {
			t.Fatalf("GetStats failed: %v", err)
		}
		if stats.PreferenceCount == 0 {
			t.Error("expected preferences")
		}
		if stats.KnowledgeCount == 0 {
			t.Error("expected knowledge")
		}
	})

	t.Run("BuildSystemPrompt", func(t *testing.T) {
		manager.SetUserPreference(ctx, "user2", "test", "key", "high_value")
		manager.AddKnowledge(ctx, "user2", "Important knowledge", "test")

		prompt, err := manager.BuildSystemPrompt(ctx, "user2", 1000)
		if err != nil {
			t.Fatalf("BuildSystemPrompt failed: %v", err)
		}
		if prompt == "" {
			t.Error("expected non-empty prompt")
		}
	})
}

func TestMultiTenantManager(t *testing.T) {
	mtm := NewMultiTenantManager(nil, nil)
	ctx := context.Background()

	t.Run("Separate User Managers", func(t *testing.T) {
		m1 := mtm.GetManager("user1")
		m2 := mtm.GetManager("user2")

		m1.SetUserPreference(ctx, "user1", "cat", "key", "value1")
		m2.SetUserPreference(ctx, "user2", "cat", "key", "value2")

		val1, _ := m1.GetUserPreference(ctx, "user1", "cat", "key")
		val2, _ := m2.GetUserPreference(ctx, "user2", "cat", "key")

		if val1 == val2 {
			t.Error("expected different values for different users")
		}
	})

	t.Run("Remove Manager", func(t *testing.T) {
		if err := mtm.RemoveManager("user1"); err != nil {
			t.Fatal(err)
		}
		if err := mtm.RemoveManager("user2"); err != nil {
			t.Fatal(err)
		}
	})
}
