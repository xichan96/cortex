package mem

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/chatstore"
	"github.com/xichan96/cortex/pkg/memkit/sqlite"
)

// user 全局合并集成测试（设计 §6.2 修正版 + 评审 B1-B4 修正）。

// 新建一个带 owner 的 ltm + 工具，模拟 factory 的归属写入。
func userMergeTool(t *testing.T, sessionID, userID string) *sqliteMemoryTool {
	t.Helper()
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	if err := ltm.SetSessionUser(context.Background(), sessionID, userID); err != nil {
		t.Fatalf("SetSessionUser: %v", err)
	}
	tool := newSQLiteMemoryTool(sessionID, ltm.Manager(), "memory", "memory_tool_write",
		defaultMemoryToolDescription, false, false).(*sqliteMemoryTool)
	tool.userID = ltm.SessionUserID(context.Background(), sessionID)
	return tool
}

// I1：session A 写 knowledge K，B（同 user）能搜到 K（跨 session 检索）。
func TestUserMergeCrossSessionSearch(t *testing.T) {
	// 同一 ltm + 同一 user，两个 session 工具。
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()

	// A、B 都归属 u1。
	if err := ltm.SetSessionUser(ctx, "sessA", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := ltm.SetSessionUser(ctx, "sessB", "u1"); err != nil {
		t.Fatal(err)
	}

	toolA := newSQLiteMemoryTool("sessA", mgr, "memory", "memory_tool_write",
		defaultMemoryToolDescription, false, false).(*sqliteMemoryTool)
	toolA.userID = "u1"
	toolB := newSQLiteMemoryTool("sessB", mgr, "memory", "memory_tool_write",
		defaultMemoryToolDescription, false, false).(*sqliteMemoryTool)
	toolB.userID = "u1"

	// A 写。
	if _, err := toolA.Execute(ctx, map[string]interface{}{
		"action": "add_knowledge", "content": "项目 API 基础路径为 /api/v1", "category": "reference",
	}); err != nil {
		t.Fatalf("A add_knowledge: %v", err)
	}
	// B 搜。
	out, err := toolB.Execute(ctx, map[string]interface{}{
		"action": "search_knowledge", "query": "api/v1",
	})
	if err != nil {
		t.Fatalf("B search: %v", err)
	}
	if s, ok := out.(string); !ok || !contains(s, "/api/v1") {
		t.Fatalf("B 应能搜到 A 写的知识, got: %v", out)
	}
}

// I2：A、B 同 user 写同 content X → 合并为 1 条、tags 合并（usage_count 不变）。
func TestUserMergeSameContentDedup(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()

	// 直接经 Manager 写（等价于工具 add_knowledge），同 user 同内容。
	if err := mgr.AddKnowledgeWithCategory(ctx, "u1", "用户喜欢 Go 语言", "project", "tagA"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, "u1", "用户喜欢 Go 语言", "project", "tagB"); err != nil {
		t.Fatal(err)
	}

	items, err := mgr.SearchKnowledge(ctx, "u1", "用户喜欢 Go 语言", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("same content should dedup to 1 row, got %d", len(items))
	}
	// tags 合并。
	if len(items[0].Tags) != 2 {
		t.Fatalf("tags should be merged [tagA tagB], got %v", items[0].Tags)
	}
}

// I3：A set_preference(cat,key,v1)，B set_preference(cat,key,v2) → B get 到 v2。
func TestUserMergePreferenceOverwrite(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()

	if err := mgr.SetUserPreference(ctx, "u1", "user", "lang", "zh"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetUserPreference(ctx, "u1", "user", "lang", "en"); err != nil {
		t.Fatal(err)
	}
	v, err := mgr.GetUserPreference(ctx, "u1", "user", "lang")
	if err != nil {
		t.Fatal(err)
	}
	if v != "en" {
		t.Fatalf("preference = %q, want en (后写覆盖)", v)
	}
}

// I4：UserMergeEnabled=false → A、B 各自写同 content X → 仍是 2 条（per-session 回归）。
func TestUserMergeDisabledPerSessionRegression(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir) // UserMergeEnabled 默认 false
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()

	if err := mgr.AddKnowledgeWithCategory(ctx, "sessA", "同名内容 X", "project"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, "sessB", "同名内容 X", "project"); err != nil {
		t.Fatal(err)
	}
	items, err := mgr.SearchKnowledge(ctx, "sessA", "同名内容 X", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("sessA own copy should be 1, got %d", len(items))
	}
	items, err = mgr.SearchKnowledge(ctx, "sessB", "同名内容 X", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("sessB own copy should be 1, got %d (per-session 语义不变)", len(items))
	}
	// 两 session 各自独立。
	items, err = mgr.SearchKnowledge(ctx, "sessA", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("sessA total = %d, want 1 (per-session)", len(items))
	}
}

// I5/M1：预置旧数据（user_id=sess_old + metadata(sess_old,'user_id','u1')）→
// 跑一次 Phase 2（含迁移 + 去重）→ 条目归拢到 u1，sess_old 名下无残留。
func TestUserMergeMigrateThenPhase2(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		t.Fatal(err)
	}
	// memkit 不建 metadata 表，手动建。
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (session_id, key)
	)`); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// 预置旧 per-session 数据。
	if err := mgr.AddKnowledgeWithCategory(ctx, "sess_old", "旧条目 A", "project", "old"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddKnowledgeWithCategory(ctx, "sess_old", "旧条目 B", "project"); err != nil {
		t.Fatal(err)
	}
	// 写归属：sess_old → u1。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO metadata (session_id, key, value) VALUES ('sess_old', 'user_id', 'u1')`); err != nil {
		t.Fatal(err)
	}
	// 预置一个「同 user 新写入的同内容」，验证迁移后去重收敛。
	if err := mgr.AddKnowledgeWithCategory(ctx, "u1", "旧条目 A", "project", "new"); err != nil {
		t.Fatal(err)
	}
	// 时间戳有精度问题，直接 SQL 更新让旧条目 updated_at 更旧。
	old := now.Add(-48 * time.Hour)
	if _, err := db.ExecContext(ctx,
		`UPDATE knowledge SET updated_at = ? WHERE tags = 'old' OR tags = ''`, old); err != nil {
		t.Fatal(err)
	}

	// 跑一次 Phase 2（含迁移 + 去重）。
	runPhase2Merge(ctx, log, mgr, func(c context.Context) (types.LLMProvider, error) {
		return noopLLM{}, nil
	}, cfg)

	// sess_old 名下无残留。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE user_id = 'sess_old'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("sess_old should have 0 rows after migration, got %d", n)
	}
	// u1 名下：旧条目 A（与 u1 新写入同内容 → 合并为 1 条）+ 旧条目 B = 2 条。
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE user_id = 'u1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("u1 should have 2 rows (A merged + B), got %d", n)
	}
	// 迁移后 search_knowledge(u1) 能搜到。
	items, err := mgr.SearchKnowledge(ctx, "u1", "旧条目", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("search u1 '旧条目' = %d, want 2", len(items))
	}
}

// I6：旧 session 无归属 → 跑迁移 → 条目不动，search_knowledge(sess_old) 仍能搜到。
func TestUserMergeMigrateSkipsUnowned(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()

	if err := mgr.AddKnowledgeWithCategory(ctx, "sess_old", "无归属条目", "project"); err != nil {
		t.Fatal(err)
	}

	// 跑迁移（无 metadata.user_id → 跳过）。
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteMigrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	items, err := mgr.SearchKnowledge(ctx, "sess_old", "无归属", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("unowned session knowledge should remain searchable, got %d", len(items))
	}
}

// M3：迁移幂等——连续跑两次，第二次条数为 0。
func TestUserMergeMigrateIdempotent(t *testing.T) {
	dir := testPersistDir(t)
	cfg := testConfig(dir)
	cfg.UserMergeEnabled = true
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ltm, err := NewLongTermMem(context.Background(), cfg, noopLLM{}, log, dir, chatstore.DefaultSharedDBFile)
	if err != nil {
		t.Fatalf("NewLongTermMem: %v", err)
	}
	ctx := context.Background()
	mgr := ltm.Manager()
	db, err := mgrDB(ctx, mgr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (session_id, key)
	)`); err != nil {
		t.Fatal(err)
	}

	if err := mgr.AddKnowledgeWithCategory(ctx, "sess_old", "迁移条目", "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO metadata (session_id, key, value) VALUES ('sess_old', 'user_id', 'u1')`); err != nil {
		t.Fatal(err)
	}

	n1, err := sqliteMigrate(ctx, db)
	if err != nil {
		t.Fatalf("migrate #1: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("migrate #1 count = %d, want 1", n1)
	}
	n2, err := sqliteMigrate(ctx, db)
	if err != nil {
		t.Fatalf("migrate #2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("migrate #2 count = %d, want 0 (idempotent)", n2)
	}
}

// 辅助：调用 memsqlite.MigrateLegacySessionKnowledge。
func sqliteMigrate(ctx context.Context, db *sql.DB) (int, error) {
	return sqlite.MigrateLegacySessionKnowledge(ctx, db)
}
