package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLitePageIndexStore_DeleteScopedByUser(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := NewSQLitePageIndexStore(st)
	const id = "p1"
	if err := p.Upsert(ctx, PageIndexDoc{ID: id, UserID: "a", Title: "t", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "b", id); err != nil {
		t.Fatal(err)
	}
	n, err := p.CountByUser(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("wrong user deleted row: n=%d", n)
	}
	if err := p.Delete(ctx, "a", id); err != nil {
		t.Fatal(err)
	}
	n2, _ := p.CountByUser(ctx, "a")
	if n2 != 0 {
		t.Fatalf("owner delete failed: n=%d", n2)
	}
}
