package mem

import (
	"strings"
	"testing"
)

func TestParseIngestItemsTab(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"end only", "END", 0},
		{"single valid", "用户喜欢 Go\tuser\tlang:go", 1},
		{"multi with end", "A\tproject\t\nB\treference\turl\nEND", 2},
		{"invalid category fallback to project", "内容\twhatever\t", 1},
		{"short line skipped", "just-onetoken", 0},
		{"code fence trimmed", "```\nX\tproject\t\n```", 1},
	}
	for _, c := range cases {
		got, err := ParseIngestItemsTab(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d items, want %d (items=%+v)", c.name, len(got), c.want, got)
		}
	}
}

func TestParseIngestItemsTabCategoryFallback(t *testing.T) {
	items, err := ParseIngestItemsTab("该项目用 Go\tunknown\t")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Category != "project" {
		t.Fatalf("category = %q, want project", items[0].Category)
	}
}

func TestParseIngestItemsTabTags(t *testing.T) {
	items, err := ParseIngestItemsTab("base path /api/v1\treference\turl:api;url:docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Tags) != 2 || items[0].Tags[0] != "url:api" || items[0].Tags[1] != "url:docs" {
		t.Fatalf("tags = %v", items[0].Tags)
	}
}

func TestIngestRuleMatches(t *testing.T) {
	r := IngestRule{
		Name:          "x",
		Action:        "count_only",
		MaxTotalChars: 100,
		PhrasesAny:    []string{"go", "sqlite"},
	}
	if !ingestRuleMatches(r, "we use Go and SQLite", 1) {
		t.Fatal("should match")
	}
	if ingestRuleMatches(r, "we use python", 1) {
		t.Fatal("should not match (no phrase)")
	}
	if ingestRuleMatches(r, strings.Repeat("a", 200), 1) {
		t.Fatal("should not match (too long)")
	}
}

func TestIsValidMemoryItem(t *testing.T) {
	f := IngestContentFilter{EnableContentFilter: true, MinContentLength: 15}
	if isValidMemoryItem(IngestExtractItem{Content: "ok"}, f) {
		t.Fatal("trivial 'ok' should be filtered")
	}
	if isValidMemoryItem(IngestExtractItem{Content: "short"}, f) {
		t.Fatal("short content without tech signal should be filtered")
	}
	if !isValidMemoryItem(IngestExtractItem{Content: "base path is https://example.com/api"}, f) {
		t.Fatal("URL content should pass")
	}
	if !isValidMemoryItem(IngestExtractItem{Content: "用户喜欢用简体中文回答问题"}, f) {
		t.Fatal("meaningful content should pass")
	}
}

func TestContainsTechSignal(t *testing.T) {
	if containsTechSignal("https://example.com") == "" {
		t.Fatal("URL should have signal")
	}
	if containsTechSignal("github.com/xichan96/cortex") == "" {
		t.Fatal("git path should have signal")
	}
	if containsTechSignal("hello") != "" {
		t.Fatal("plain word should have no signal")
	}
}
