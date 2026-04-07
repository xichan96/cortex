package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIngestCursorAndStat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()

	if err := IngestSetCursor(ctx, db, "s1", 10); err != nil {
		t.Fatal(err)
	}
	n, err := IngestGetCursor(ctx, db, "s1")
	if err != nil || n != 10 {
		t.Fatalf("cursor got %d err %v", n, err)
	}
	if err := IngestIncStat(ctx, db, "s1", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := IngestIncStat(ctx, db, "s1", "r1"); err != nil {
		t.Fatal(err)
	}
	var c int
	if err := db.QueryRowContext(ctx, `SELECT count FROM memory_ingest_stats WHERE session_id='s1' AND rule_name='r1'`).Scan(&c); err != nil || c != 2 {
		t.Fatalf("stat count=%d err=%v", c, err)
	}
}
