package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type SQLitePageIndexStore struct {
	db *sql.DB
}

func NewSQLitePageIndexStore(store *SQLiteStore) *SQLitePageIndexStore {
	return &SQLitePageIndexStore{db: store.DB()}
}

func (s *SQLitePageIndexStore) Upsert(ctx context.Context, doc PageIndexDoc) error {
	if doc.UserID == "" {
		return fmt.Errorf("memory: page_index user_id required")
	}
	if doc.Text == "" && doc.Title == "" {
		return fmt.Errorf("memory: page_index title or text required")
	}
	if doc.ID == "" {
		doc.ID = utils.NewID()
	}
	now := time.Now()
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	doc.UpdatedAt = now
	if doc.Priority == 0 {
		doc.Priority = PriorityMedium
	}
	meta, _ := safeJSONMarshal(doc.Metadata)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO page_index (id, user_id, kind, title, text, ref_kind, ref_id, metadata, priority, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 user_id=excluded.user_id, kind=excluded.kind, title=excluded.title, text=excluded.text,
		 ref_kind=excluded.ref_kind, ref_id=excluded.ref_id, metadata=excluded.metadata,
		 priority=excluded.priority, updated_at=excluded.updated_at`,
		doc.ID, doc.UserID, string(doc.Kind), doc.Title, doc.Text, doc.RefKind, doc.RefID,
		string(meta), int(doc.Priority), doc.CreatedAt, doc.UpdatedAt)
	return err
}

func (s *SQLitePageIndexStore) Delete(ctx context.Context, userID, id string) error {
	if userID == "" {
		return fmt.Errorf("memory: page_index user_id required")
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM page_index WHERE id = ? AND user_id = ?", id, userID)
	return err
}

func (s *SQLitePageIndexStore) DeleteByUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM page_index WHERE user_id = ?", userID)
	return err
}

func (s *SQLitePageIndexStore) DeleteByKinds(ctx context.Context, userID string, kinds []PageIndexKind) error {
	if len(kinds) == 0 {
		return nil
	}
	placeholders := make([]string, len(kinds))
	args := make([]interface{}, 0, len(kinds)+1)
	args = append(args, userID)
	for i, k := range kinds {
		placeholders[i] = "?"
		args = append(args, string(k))
	}
	q := fmt.Sprintf("DELETE FROM page_index WHERE user_id = ? AND kind IN (%s)", strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *SQLitePageIndexStore) CountByUser(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM page_index WHERE user_id = ?", userID).Scan(&n)
	return n, err
}

func (s *SQLitePageIndexStore) scanDoc(rows *sql.Rows) (PageIndexDoc, error) {
	var d PageIndexDoc
	var kind, meta string
	var pr int
	err := rows.Scan(&d.ID, &d.UserID, &kind, &d.Title, &d.Text, &d.RefKind, &d.RefID, &meta, &pr, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	d.Kind = PageIndexKind(kind)
	d.Priority = Priority(pr)
	_ = safeJSONUnmarshal(meta, &d.Metadata)
	return d, nil
}

func (s *SQLitePageIndexStore) Search(ctx context.Context, userID, query string, opts *PageIndexSearchOptions) ([]PageIndexHit, error) {
	limit := 12
	minScore := 0.0
	var kinds []PageIndexKind
	if opts != nil {
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		minScore = opts.MinScore
		kinds = opts.Kinds
	}
	q := `SELECT id, user_id, kind, title, text, ref_kind, ref_id, metadata, priority, created_at, updated_at FROM page_index WHERE user_id = ?`
	args := []interface{}{userID}
	if len(kinds) > 0 {
		ph := make([]string, len(kinds))
		for i, k := range kinds {
			ph[i] = "?"
			args = append(args, string(k))
		}
		q += " AND kind IN (" + strings.Join(ph, ",") + ")"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []PageIndexHit
	for rows.Next() {
		d, err := s.scanDoc(rows)
		if err != nil {
			return nil, err
		}
		sc := utils.PageKeywordScore(query, d.Title, d.Text)
		if sc < minScore {
			continue
		}
		hits = append(hits, PageIndexHit{Doc: d, Score: sc})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Doc.Priority != hits[j].Doc.Priority {
			return hits[i].Doc.Priority > hits[j].Doc.Priority
		}
		return hits[i].Doc.UpdatedAt.After(hits[j].Doc.UpdatedAt)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
