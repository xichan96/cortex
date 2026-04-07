package stores

import (
	"context"
	"testing"
)

func TestNormalizeContent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  Hello   World  ", "hello world"},
		{"\n\tMulti\n\tLine  ", "multi line"},
		{"  LEADING  ", "leading"},
		{"  TRAILING  ", "trailing"},
		{"NoChange", "nochange"},
		{"  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeContent(tt.input)
			if got != tt.want {
				t.Errorf("normalizeContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInMemoryKnowledgeStore_Deduplication(t *testing.T) {
	store := NewInMemoryKnowledgeStore()
	ctx := context.Background()
	userID := "test-user"

	err := store.Add(ctx, KnowledgeEntry{
		UserID:  userID,
		Content: "User prefers dark mode",
		Tags:    []string{"ui"},
	})
	if err != nil {
		t.Fatalf("first Add failed: %v", err)
	}

	stats, _ := store.GetStats(ctx, userID)
	if stats != 1 {
		t.Errorf("expected 1 entry, got %d", stats)
	}

	err = store.Add(ctx, KnowledgeEntry{
		UserID:  userID,
		Content: "  user  prefers   dark  mode  ",
		Tags:    []string{"preference"},
	})
	if err != nil {
		t.Fatalf("second Add failed: %v", err)
	}

	result, _ := store.Search(ctx, userID, &SearchOptions{Limit: 10})
	if len(result.Items) != 1 {
		t.Errorf("expected 1 item after dedup, got %d", len(result.Items))
	}

	item := result.Items[0]
	hasUI := false
	hasPref := false
	for _, tag := range item.Tags {
		if tag == "ui" {
			hasUI = true
		}
		if tag == "preference" {
			hasPref = true
		}
	}
	if !hasUI || !hasPref {
		t.Errorf("expected tags to be merged [ui, preference], got %v", item.Tags)
	}

	err = store.Add(ctx, KnowledgeEntry{
		UserID:  userID,
		Content: "Uses VSCode editor",
		Tags:    []string{"editor"},
	})
	if err != nil {
		t.Fatalf("third Add failed: %v", err)
	}

	result2, _ := store.Search(ctx, userID, &SearchOptions{Limit: 10})
	if len(result2.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result2.Items))
	}
}

func TestUnionStrings(t *testing.T) {
	tests := []struct {
		a, b []string
		want []string
	}{
		{[]string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{[]string{}, []string{"x"}, []string{"x"}},
		{[]string{"a"}, []string{}, []string{"a"}},
		{[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		got := unionStrings(tt.a, tt.b)
		if len(got) != len(tt.want) {
			t.Errorf("unionStrings(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("unionStrings(%v, %v)[%d] = %v, want %v", tt.a, tt.b, i, got[i], tt.want[i])
			}
		}
	}
}
