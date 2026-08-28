package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	return store
}

// 迁移幂等：knowledge 增加 usage_count/last_usage，重跑 migrate 不炸（R4）。
func TestMigrationIdempotent(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	// 列存在。
	if !columnExists(db, "knowledge", "usage_count") {
		t.Fatal("usage_count column missing after migrate")
	}
	if !columnExists(db, "knowledge", "last_usage") {
		t.Fatal("last_usage column missing after migrate")
	}
	// memory_phase2_lock 表存在。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memory_phase2_lock'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("memory_phase2_lock table missing")
	}

	// 重跑 migrate 不炸。
	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestRecordKnowledgeUse(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	uid := "u1"
	content := "用户喜欢 Go 语言"
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at)
		 VALUES (?, ?, 'project', ?, '', '', '', 5, ?, ?)`,
		utils.NewID(), uid, content, time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM knowledge WHERE user_id = ? LIMIT 1`, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}

	ks := NewSQLiteKnowledgeStore(store)
	if err := ks.RecordKnowledgeUse(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := ks.RecordKnowledgeUse(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	var usage int
	var lastUsage *time.Time
	if err := db.QueryRow(`SELECT usage_count, last_usage FROM knowledge WHERE id = ?`, id).Scan(&usage, &lastUsage); err != nil {
		t.Fatal(err)
	}
	if usage != 2 {
		t.Fatalf("usage_count = %d, want 2", usage)
	}
	if lastUsage == nil {
		t.Fatal("last_usage should be set")
	}
}

func TestSearchKnowledgeSQLPushdown(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	uid := "u2"
	now := time.Now()
	rows := []struct {
		id, content string
	}{
		{utils.NewID(), "该项目使用 PostgreSQL 作为数据库"},
		{utils.NewID(), "用户偏好使用 Vim 编辑器"},
		{utils.NewID(), "API 文档在 docs/README.md"},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at)
			 VALUES (?, ?, 'project', ?, '', '', '', 5, ?, ?)`,
			r.id, uid, r.content, now, now); err != nil {
			t.Fatal(err)
		}
	}

	ks := NewSQLiteKnowledgeStore(store)
	res, err := ks.Search(context.Background(), uid, &SearchOptions{Query: "postgres", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 match for 'postgres', got %d", len(res.Items))
	}
	if res.Items[0].Value != "该项目使用 PostgreSQL 作为数据库" {
		t.Fatalf("wrong match: %s", res.Items[0].Value)
	}
	if res.Total != 1 {
		t.Fatalf("total = %d, want 1", res.Total)
	}
}

// 排序：无 query 时按 usage_count > priority > updated_at；同条件按 updated_at DESC。
func TestSearchKnowledgeOrderByUsage(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	uid := "u3"
	now := time.Now()
	mk := func(content string, older bool, usage int) string {
		id := utils.NewID()
		ts := now
		if older {
			ts = now.Add(-time.Hour)
		}
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, usage_count, created_at, updated_at)
			 VALUES (?, ?, 'project', ?, '', '', '', 5, ?, ?, ?)`,
			id, uid, content, usage, ts, ts); err != nil {
			t.Fatal(err)
		}
		return id
	}
	usedID := mk("常用条目", false, 3)
	freshID := mk("新条目", false, 0)
	oldID := mk("旧条目", true, 0)

	ks := NewSQLiteKnowledgeStore(store)
	res, err := ks.Search(context.Background(), uid, &SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 期望顺序：usedID (usage 3) > freshID (usage 0, newer) > oldID (usage 0, older)
	if len(res.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(res.Items))
	}
	if res.Items[0].ID != usedID {
		t.Fatalf("first should be usedID (usage), got %s", res.Items[0].ID)
	}
	if res.Items[1].ID != freshID {
		t.Fatalf("second should be freshID (newer), got %s", res.Items[1].ID)
	}
	if res.Items[2].ID != oldID {
		t.Fatalf("third should be oldID (older), got %s", res.Items[2].ID)
	}
}

// Phase 2 全局锁：未持锁可拿、持有中不可拿、lease 过期可重拿、冷却内不可拿。
func TestTryClaimPhase2(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	ctx := context.Background()
	lease := time.Minute
	cooldown := time.Hour

	// 首次认领成功。
	ok, err := TryClaimPhase2(ctx, db, "h1", lease, cooldown)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("first claim should succeed")
	}

	// 持有中不可拿。
	ok, err = TryClaimPhase2(ctx, db, "h2", lease, cooldown)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second claim while held should fail")
	}

	// 冷却内不可拿（即使租约过期）。
	ok, err = TryClaimPhase2(ctx, db, "h3", lease, cooldown)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("claim during cooldown should fail")
	}

	// 手动清掉 holder 与 cooldown（模拟长期无更新）后，可重拿。
	if _, err := db.ExecContext(ctx,
		`UPDATE memory_phase2_lock SET holder = NULL, lease_until = NULL, cooldown_until = NULL WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	ok, err = TryClaimPhase2(ctx, db, "h4", lease, cooldown)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("claim after release should succeed")
	}
}

func TestPruneUnused(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()

	ctx := context.Background()
	uid := "u4"
	now := time.Now()
	insert := func(content string, daysAgo int, usage int, used bool) string {
		id := utils.NewID()
		ts := now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
		var lastUsage interface{}
		if used {
			lastUsage = now
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, usage_count, last_usage, created_at, updated_at)
			 VALUES (?, ?, 'project', ?, '', '', '', 5, ?, ?, ?, ?)`,
			id, uid, content, usage, lastUsage, ts, ts); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 40 天前、未使用 → 应被删。
	oldID := insert("很旧的未用条目", 40, 0, false)
	// 40 天前、有 usage → 豁免保留。
	usedID := insert("很旧但被引用过", 40, 1, true)
	// 10 天前、未使用 → 保留。
	freshID := insert("最近的未用条目", 10, 0, false)

	if err := PruneUnused(ctx, db, 30); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE user_id = ?`, uid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows after prune (old-used + fresh), got %d", count)
	}
	for _, id := range []string{usedID, freshID} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected %s to survive prune", id)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE id = ?`, oldID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("old unused entry should be pruned")
	}
}

var _ = sql.ErrNoRows
