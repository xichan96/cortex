package mem

import (
	"context"
	"testing"

	"github.com/xichan96/cortex/dino/chatstore"
)

func testTool(t *testing.T, exposeIndexes bool) *sqliteMemoryTool {
	t.Helper()
	dir := t.TempDir()
	mgr, err := SharedSQLiteManager(dir, chatstore.DefaultSharedDBFile)
	if err != nil || mgr == nil {
		t.Fatalf("manager: %v", err)
	}
	tt := newSQLiteMemoryTool("s1", mgr, "memory", "memory_tool_write",
		defaultMemoryToolDescription, exposeIndexes, false)
	tool, ok := tt.(*sqliteMemoryTool)
	if !ok {
		t.Fatal("expected *sqliteMemoryTool")
	}
	return tool
}

func actionsInSchema(t *testing.T, tool *sqliteMemoryTool) map[string]bool {
	t.Helper()
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}
	act, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no action property")
	}
	enum, ok := act["enum"].([]string)
	if !ok {
		t.Fatal("action enum not []string")
	}
	out := make(map[string]bool)
	for _, a := range enum {
		out[a] = true
	}
	return out
}

func TestMemoryToolActionsSplit(t *testing.T) {
	tool := testTool(t, false)
	actions := actionsInSchema(t, tool)

	// 模型可见：读 + 写 + forget。
	for _, a := range []string{"get_preference", "list_preferences", "search_knowledge", "memory_stats", "set_preference", "add_knowledge", "forget_knowledge", "forget_preference"} {
		if !actions[a] {
			t.Errorf("expected action %q to be visible", a)
		}
	}
	// build_system_prompt 已移除（评审 §2.4）。
	if actions["build_system_prompt"] {
		t.Error("build_system_prompt should NOT be model-visible")
	}
	// search_indexes 默认不暴露。
	if actions["search_indexes"] {
		t.Error("search_indexes should be hidden by default")
	}
}

func TestMemoryToolSearchIndexesGated(t *testing.T) {
	tool := testTool(t, true)
	actions := actionsInSchema(t, tool)
	if !actions["search_indexes"] {
		t.Error("search_indexes should be visible when ExposeSearchIndexes=true")
	}
}

func TestMemoryToolForgetKnowledge(t *testing.T) {
	tool := testTool(t, false)
	ctx := context.Background()
	uid := "s1"

	if _, err := tool.Execute(ctx, map[string]interface{}{"action": "add_knowledge", "content": "待删除的知识", "category": "project"}); err != nil {
		t.Fatalf("add_knowledge: %v", err)
	}
	items, err := tool.mgr.SearchKnowledge(ctx, uid, "待删除", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 knowledge, got %d", len(items))
	}
	id := items[0].ID

	if _, err := tool.Execute(ctx, map[string]interface{}{"action": "forget_knowledge", "id": id}); err != nil {
		t.Fatalf("forget_knowledge: %v", err)
	}
	items, err = tool.mgr.SearchKnowledge(ctx, uid, "待删除", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 knowledge after forget, got %d", len(items))
	}
}

func TestMemoryToolForgetPreference(t *testing.T) {
	tool := testTool(t, false)
	ctx := context.Background()
	uid := "s1"

	if _, err := tool.Execute(ctx, map[string]interface{}{"action": "set_preference", "category": "user", "key": "lang", "value": "zh"}); err != nil {
		t.Fatalf("set_preference: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]interface{}{"action": "forget_preference", "category": "user", "key": "lang"}); err != nil {
		t.Fatalf("forget_preference: %v", err)
	}
	v, err := tool.mgr.GetUserPreference(ctx, uid, "user", "lang")
	if err != nil {
		t.Fatal(err)
	}
	if v != "" {
		t.Fatalf("preference should be deleted, got %q", v)
	}
}

// TestExternalContextTool verifies the E6 external-context classifier
// (tools-codex-eval §6.3): web_* and MCP tools are external-context sources.
func TestExternalContextTool(t *testing.T) {
	external := []string{"web_search", "web_fetch", "mcp://github/tool"}
	for _, name := range external {
		if !ExternalContextTool(name) {
			t.Errorf("ExternalContextTool(%q) = false, want true", name)
		}
	}
	internal := []string{"read_file", "bash", "grep", "memory", ""}
	for _, name := range internal {
		if ExternalContextTool(name) {
			t.Errorf("ExternalContextTool(%q) = true, want false", name)
		}
	}
}

// TestAddKnowledgeExternalContextRejectsUserFeedback verifies the E6 rule:
// add_knowledge with external_context=true may only be stored as reference,
// not user/feedback.
func TestAddKnowledgeExternalContextRejectsUserFeedback(t *testing.T) {
	tool := testTool(t, false)
	// external_context=true + category=user → rejected.
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":           "add_knowledge",
		"content":          "the sky is blue per a web page",
		"category":         "user",
		"external_context": true,
	})
	if err == nil {
		t.Fatal("external_context user write should be rejected")
	}
	// external_context=true + category=reference → accepted.
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":           "add_knowledge",
		"content":          "the sky is blue per a web page",
		"category":         "reference",
		"external_context": true,
	}); err != nil {
		t.Fatalf("external_context reference write should be accepted: %v", err)
	}
	// external_context unset (default false) + category=user → accepted.
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "add_knowledge",
		"content":  "I prefer Go",
		"category": "user",
	}); err != nil {
		t.Fatalf("non-external user write should be accepted: %v", err)
	}
}
