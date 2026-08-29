package mem

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/xichan96/cortex/dino/chatstore"
)

func TestResolveUserID(t *testing.T) {
	cases := []struct {
		name    string
		session string
		cfg     string
		want    string
	}{
		{"explicit wins", "u1", "cfg", "u1"},
		{"config fallback", "", "cfg", "cfg"},
		{"fallback constant", "", "", defaultUserIDFallback},
		{"empty explicit falls to cfg", "", "cfg2", "cfg2"},
	}
	for _, c := range cases {
		if got := ResolveUserID(c.session, c.cfg); got != c.want {
			t.Errorf("%s: ResolveUserID(%q, %q) = %q, want %q", c.name, c.session, c.cfg, got, c.want)
		}
	}
}

// UserIDForSession：有归属返回归属，无归属回退 sessionID（单一事实源）。
func TestUserIDForSession(t *testing.T) {
	dir := testPersistDir(t)
	mgr, err := SharedSQLiteManager(dir, chatstore.DefaultSharedDBFile)
	if err != nil || mgr == nil {
		t.Fatalf("manager: %v", err)
	}
	ctx := context.Background()
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		t.Fatal(err)
	}
	// memkit migrate 不建 metadata 表；手动建（与 chatstore 同结构）。
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (session_id, key)
	)`); err != nil {
		t.Fatal(err)
	}

	// 无归属 → sessionID。
	if got := UserIDForSession(ctx, db, "sessA"); got != "sessA" {
		t.Fatalf("no owner: got %q, want sessA", got)
	}

	// 有归属 → owner。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO metadata (session_id, key, value) VALUES ('sessA', 'user_id', 'u1')`); err != nil {
		t.Fatal(err)
	}
	if got := UserIDForSession(ctx, db, "sessA"); got != "u1" {
		t.Fatalf("with owner: got %q, want u1", got)
	}
}

// T2：SetSessionUser 写 metadata 'user_id'；重复调用不覆盖（INSERT OR IGNORE）。
func TestSetSessionUser(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()

	if err := ltm.SetSessionUser(ctx, "sessA", "u1"); err != nil {
		t.Fatalf("SetSessionUser #1: %v", err)
	}
	if err := ltm.SetSessionUser(ctx, "sessA", "u2"); err != nil {
		t.Fatalf("SetSessionUser #2: %v", err)
	}
	// 已固化，第二次不覆盖。
	if got := ltm.SessionUserID(ctx, "sessA"); got != "u1" {
		t.Fatalf("owner after second set = %q, want u1 (INSERT OR IGNORE)", got)
	}
}

// 集成验证工具 uid：WithUserID 传 userID、空回退 sessionID；probe 仍按 session 登记。
func TestMemoryToolUserIDResolution(t *testing.T) {
	dir := testPersistDir(t)
	mgr, err := SharedSQLiteManager(dir, chatstore.DefaultSharedDBFile)
	if err != nil || mgr == nil {
		t.Fatalf("manager: %v", err)
	}
	ctx := context.Background()

	// 构造带 userID 的工具。
	toolWithUser := newSQLiteMemoryTool("s1", mgr, "memory", "memory_tool_write",
		defaultMemoryToolDescription, false, false).(*sqliteMemoryTool)
	toolWithUser.userID = "u1"

	if _, err := toolWithUser.Execute(ctx, map[string]interface{}{
		"action": "add_knowledge", "content": "user 全局的知识", "category": "project",
	}); err != nil {
		t.Fatalf("add_knowledge: %v", err)
	}
	// 写入落在 userID=u1 名下。
	items, err := mgr.SearchKnowledge(ctx, "u1", "user 全局", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("search u1: want 1, got %d", len(items))
	}
	// sessionID=s1 名下无条目。
	items, err = mgr.SearchKnowledge(ctx, "s1", "user 全局", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("search s1: want 0, got %d (per-session 隔离)", len(items))
	}

	// 空 userID 的工具回退 sessionID。
	toolNoUser := newSQLiteMemoryTool("s2", mgr, "memory", "memory_tool_write",
		defaultMemoryToolDescription, false, false).(*sqliteMemoryTool)
	if _, err := toolNoUser.Execute(ctx, map[string]interface{}{
		"action": "add_knowledge", "content": "s2 专属知识", "category": "project",
	}); err != nil {
		t.Fatalf("add_knowledge: %v", err)
	}
	items, err = mgr.SearchKnowledge(ctx, "s2", "s2 专属", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("search s2: want 1, got %d", len(items))
	}
}
