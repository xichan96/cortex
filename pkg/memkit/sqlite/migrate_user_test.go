package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

// 迁移测试用：memkit 的 migrate() 不建 metadata 表（那是 chatstore 的 shared DB
// 负责的），测试里手动建一张与 chatstore 同结构的表。
func ensureMetadataTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (session_id, key)
	)`); err != nil {
		t.Fatalf("create metadata: %v", err)
	}
}

func insertSessionOwner(t *testing.T, db *sql.DB, sessionID, owner string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO metadata (session_id, key, value) VALUES (?, 'user_id', ?)`,
		sessionID, owner); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
}

func insertKnowledge(t *testing.T, db *sql.DB, id, userID, content string, updatedAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at)
		 VALUES (?, ?, 'project', ?, '', '', 'test', 5, ?, ?)`,
		id, userID, content, updatedAt, updatedAt); err != nil {
		t.Fatalf("insert knowledge: %v", err)
	}
}

func insertPreference(t *testing.T, db *sql.DB, id, userID, cat, key, value string, updatedAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO preferences (id, user_id, category, key, value, priority, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 5, '', ?, ?)`,
		id, userID, cat, key, value, updatedAt, updatedAt); err != nil {
		t.Fatalf("insert preference: %v", err)
	}
}

func countKnowledge(t *testing.T, db *sql.DB, userID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM knowledge WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count knowledge: %v", err)
	}
	return n
}

func TestMigrateLegacySessionKnowledge(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ensureMetadataTable(t, db)
	ctx := context.Background()

	now := time.Now()
	// sessA 归属 u1，名下两条 knowledge + 一条 preference。
	insertSessionOwner(t, db, "sessA", "u1")
	insertKnowledge(t, db, "k1", "sessA", "A 的知识", now.Add(-time.Hour))
	insertKnowledge(t, db, "k2", "sessA", "A 的另一条", now.Add(-2*time.Hour))
	insertPreference(t, db, "p1", "sessA", "user", "lang", "zh", now.Add(-time.Hour))
	// sessB 归属 u2。
	insertSessionOwner(t, db, "sessB", "u2")
	insertKnowledge(t, db, "k3", "sessB", "B 的知识", now.Add(-time.Hour))
	// sessC 无归属：应跳过。
	insertKnowledge(t, db, "k4", "sessC", "C 的知识", now.Add(-time.Hour))

	n, err := MigrateLegacySessionKnowledge(ctx, db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// k1/k2 + p1 归拢（3 条），k3 也归拢（sessB → u2，1 条），sessC 跳过。
	if n != 4 {
		t.Fatalf("migrated count = %d, want 4", n)
	}

	// 归属校验。
	if countKnowledge(t, db, "sessA") != 0 {
		t.Fatal("sessA 名下应无残留 knowledge")
	}
	if countKnowledge(t, db, "u1") != 2 {
		t.Fatalf("u1 应有 2 条 knowledge, got %d", countKnowledge(t, db, "u1"))
	}
	if countKnowledge(t, db, "u2") != 1 {
		t.Fatalf("u2 应有 1 条 knowledge, got %d", countKnowledge(t, db, "u2"))
	}
	if countKnowledge(t, db, "sessC") != 1 {
		t.Fatal("无归属 sessC 的条目应保持原样")
	}
	var prefUser string
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM preferences WHERE id = 'p1'`).Scan(&prefUser); err != nil {
		t.Fatal(err)
	}
	if prefUser != "u1" {
		t.Fatalf("p1 user_id = %q, want u1", prefUser)
	}
}

func TestMigrateLegacySessionKnowledgeIdempotent(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ensureMetadataTable(t, db)
	ctx := context.Background()

	now := time.Now()
	insertSessionOwner(t, db, "sessA", "u1")
	insertKnowledge(t, db, "k1", "sessA", "A 的知识", now.Add(-time.Hour))
	insertPreference(t, db, "p1", "sessA", "user", "lang", "zh", now.Add(-time.Hour))

	n1, err := MigrateLegacySessionKnowledge(ctx, db)
	if err != nil {
		t.Fatalf("migrate #1: %v", err)
	}
	if n1 != 2 {
		t.Fatalf("migrate #1 count = %d, want 2", n1)
	}
	// 第二次：sessA 名下已无条目，候选 uid 不再含 sessA → 0。
	n2, err := MigrateLegacySessionKnowledge(ctx, db)
	if err != nil {
		t.Fatalf("migrate #2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("migrate #2 count = %d, want 0 (idempotent)", n2)
	}
}

// 评审 B2：两个被合并 session 有同 (category, key) 的 preference，迁移不撞
// UNIQUE(user_id, category, key)，保留 updated_at 较新者（后写覆盖）。
func TestMigrateLegacyPreferenceConflictLatestWins(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ensureMetadataTable(t, db)
	ctx := context.Background()

	now := time.Now()
	insertSessionOwner(t, db, "sessA", "u1")
	insertSessionOwner(t, db, "sessB", "u1")
	// 同 (user, lang) 键：A 较旧、B 较新。
	insertPreference(t, db, "pa", "sessA", "user", "lang", "zh", now.Add(-2*time.Hour))
	insertPreference(t, db, "pb", "sessB", "user", "lang", "en", now.Add(-time.Hour))

	if _, err := MigrateLegacySessionKnowledge(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var value string
	var pid string
	if err := db.QueryRowContext(ctx,
		`SELECT id, value FROM preferences WHERE user_id = 'u1' AND category = 'user' AND key = 'lang'`).
		Scan(&pid, &value); err != nil {
		t.Fatalf("read merged preference: %v", err)
	}
	if value != "en" {
		t.Fatalf("merged value = %q, want en (较新者胜)", value)
	}
	if pid != "pb" {
		t.Fatalf("kept id = %q, want pb", pid)
	}
	// 无重复行。
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM preferences WHERE user_id = 'u1' AND category = 'user' AND key = 'lang'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("preference rows = %d, want 1", n)
	}
}

// 归属指向自己（u==uid）：跳过，不产生无意义 UPDATE。
func TestMigrateLegacySelfOwnerSkipped(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ensureMetadataTable(t, db)
	ctx := context.Background()

	now := time.Now()
	// sessA 的归属就是它自己（已归拢过的 user_id 恰好是 "sessA"）。
	insertSessionOwner(t, db, "sessA", "sessA")
	insertKnowledge(t, db, "k1", "sessA", "A 的知识", now.Add(-time.Hour))

	n, err := MigrateLegacySessionKnowledge(ctx, db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n != 0 {
		t.Fatalf("self-owner should be skipped, migrated %d", n)
	}
	if countKnowledge(t, db, "sessA") != 1 {
		t.Fatal("self-owned knowledge should remain")
	}
}

// M1：迁移归拢后同 user 同 content 由 DedupUserKnowledge 收敛为 1 条、tags 并。
func TestMigrateThenDedupConverges(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ensureMetadataTable(t, db)
	ctx := context.Background()

	now := time.Now()
	insertSessionOwner(t, db, "sessA", "u1")
	insertSessionOwner(t, db, "sessB", "u1")
	// A、B 各有一条同 content（迁移归拢后同 user 重复）。
	insertKnowledge(t, db, "ka", "sessA", "用户喜欢 Go 语言", now.Add(-2*time.Hour))
	insertKnowledge(t, db, "kb", "sessB", "用户喜欢 Go 语言", now.Add(-time.Hour))
	// 给 kb 打 tags，验证合并。
	if _, err := db.ExecContext(ctx,
		`UPDATE knowledge SET tags = 'lang:go' WHERE id = 'kb'`); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateLegacySessionKnowledge(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	deleted, err := DedupUserKnowledge(ctx, db, "u1")
	if err != nil {
		t.Fatalf("dedup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("dedup deleted = %d, want 1", deleted)
	}
	if countKnowledge(t, db, "u1") != 1 {
		t.Fatalf("u1 should have 1 knowledge after dedup, got %d", countKnowledge(t, db, "u1"))
	}
	// 保留的是 updated_at 较新的一条（kb）。
	var keptID, tags string
	if err := db.QueryRowContext(ctx,
		`SELECT id, tags FROM knowledge WHERE user_id = 'u1'`).Scan(&keptID, &tags); err != nil {
		t.Fatal(err)
	}
	if keptID != "kb" {
		t.Fatalf("kept id = %q, want kb (较新)", keptID)
	}
	if !containsTag(tags, "lang:go") {
		t.Fatalf("tags = %q, want merged lang:go", tags)
	}
}

func containsTag(tags, tag string) bool {
	for _, t := range parseTagsFromDB(tags) {
		if t == tag {
			return true
		}
	}
	return false
}

// task B2/B3：DedupUserKnowledge 保留 updated_at 最新、不刷平。
func TestDedupUserKnowledgePreservesUpdatedAt(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	db := store.DB()
	ctx := context.Background()

	old := time.Now().Add(-20 * 24 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	insertKnowledge(t, db, utils.NewID(), "u1", "同一条内容", old)
	insertKnowledge(t, db, utils.NewID(), "u1", "同一条内容", newer)

	deleted, err := DedupUserKnowledge(ctx, db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	// 保留的是较新的 updated_at，且没被刷成 now。
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx,
		`SELECT updated_at FROM knowledge WHERE user_id = 'u1'`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if !updatedAt.Equal(newer) {
		t.Fatalf("updated_at = %v, want %v (保留原值不刷平)", updatedAt, newer)
	}
}
