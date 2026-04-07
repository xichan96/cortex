package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

func stringMapToInterface(m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}

type SQLitePreferenceStore struct {
	db *sql.DB
}

func NewSQLitePreferenceStore(store *SQLiteStore) *SQLitePreferenceStore {
	return &SQLitePreferenceStore{db: store.DB()}
}

func (s *SQLitePreferenceStore) Set(ctx context.Context, pref Preference) error {
	if pref.Category == "" {
		pref.Category = "default"
	}
	if pref.ID == "" {
		pref.ID = utils.NewID()
	}
	if pref.CreatedAt.IsZero() {
		pref.CreatedAt = time.Now()
	}
	pref.UpdatedAt = time.Now()

	metadata, _ := safeJSONMarshal(pref.Metadata)

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO preferences (id, user_id, category, key, value, priority, metadata, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pref.ID, pref.UserID, pref.Category, pref.Key, pref.Value, pref.Priority, string(metadata), pref.CreatedAt, pref.UpdatedAt)
	return err
}

func (s *SQLitePreferenceStore) Get(ctx context.Context, userID, category, key string) (*Preference, error) {
	if category == "" {
		category = "default"
	}

	var pref Preference
	var metadata string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, category, key, value, priority, metadata, created_at, updated_at 
         FROM preferences WHERE user_id = ? AND category = ? AND key = ?`,
		userID, category, key).Scan(
		&pref.ID, &pref.UserID, &pref.Category, &pref.Key, &pref.Value,
		&pref.Priority, &metadata, &pref.CreatedAt, &pref.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	safeJSONUnmarshal(metadata, &pref.Metadata)
	return &pref, nil
}

func (s *SQLitePreferenceStore) GetByUser(ctx context.Context, userID string) ([]Preference, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category, key, value, priority, metadata, created_at, updated_at 
         FROM preferences WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prefs []Preference
	for rows.Next() {
		var pref Preference
		var metadata string
		if err := rows.Scan(&pref.ID, &pref.UserID, &pref.Category, &pref.Key, &pref.Value,
			&pref.Priority, &metadata, &pref.CreatedAt, &pref.UpdatedAt); err != nil {
			return nil, err
		}
		safeJSONUnmarshal(metadata, &pref.Metadata)
		prefs = append(prefs, pref)
	}
	return prefs, rows.Err()
}

func (s *SQLitePreferenceStore) GetByCategory(ctx context.Context, userID, category string) ([]Preference, error) {
	if category == "" {
		category = "default"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category, key, value, priority, metadata, created_at, updated_at 
         FROM preferences WHERE user_id = ? AND category = ?`, userID, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prefs []Preference
	for rows.Next() {
		var pref Preference
		var metadata string
		if err := rows.Scan(&pref.ID, &pref.UserID, &pref.Category, &pref.Key, &pref.Value,
			&pref.Priority, &metadata, &pref.CreatedAt, &pref.UpdatedAt); err != nil {
			return nil, err
		}
		safeJSONUnmarshal(metadata, &pref.Metadata)
		prefs = append(prefs, pref)
	}
	return prefs, rows.Err()
}

func (s *SQLitePreferenceStore) Delete(ctx context.Context, userID, category, key string) error {
	if category == "" {
		category = "default"
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM preferences WHERE user_id = ? AND category = ? AND key = ?",
		userID, category, key)
	return err
}

func (s *SQLitePreferenceStore) Clear(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM preferences WHERE user_id = ?", userID)
	return err
}

func (s *SQLitePreferenceStore) Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error) {
	query := `SELECT id, user_id, category, key, value, priority, metadata, created_at, updated_at 
              FROM preferences WHERE user_id = ?`
	args := []interface{}{userID}

	if opts != nil && opts.Category != "" {
		query += " AND category = ?"
		args = append(args, opts.Category)
	}
	query += " ORDER BY priority DESC, updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MemoryItem
	for rows.Next() {
		var pref Preference
		var metadata string
		if err := rows.Scan(&pref.ID, &pref.UserID, &pref.Category, &pref.Key, &pref.Value,
			&pref.Priority, &metadata, &pref.CreatedAt, &pref.UpdatedAt); err != nil {
			return nil, err
		}
		_ = safeJSONUnmarshal(metadata, &pref.Metadata)
		if opts != nil {
			if opts.Priority != nil && pref.Priority < *opts.Priority {
				continue
			}
			if len(opts.Tags) > 0 && !hasIntersection(pref.Metadata["tags"], opts.Tags) {
				continue
			}
			if opts.Query != "" {
				q := strings.ToLower(opts.Query)
				if !strings.Contains(strings.ToLower(pref.Key), q) && !strings.Contains(strings.ToLower(pref.Value), q) {
					continue
				}
			}
		}
		items = append(items, MemoryItem{
			ID:        pref.ID,
			Type:      MemoryTypePreference,
			Key:       pref.Key,
			Value:     pref.Value,
			Priority:  pref.Priority,
			Metadata:  stringMapToInterface(pref.Metadata),
			CreatedAt: pref.CreatedAt,
			UpdatedAt: pref.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	total := len(items)
	offset, limit := 0, 50
	if opts != nil {
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		if opts.Offset > 0 {
			offset = opts.Offset
		}
	}
	hasMore := offset+limit < total
	if offset >= total {
		items = nil
	} else if offset+limit < total {
		items = items[offset : offset+limit]
	} else {
		items = items[offset:]
	}

	return &SearchResult{Items: items, Total: total, HasMore: hasMore}, nil
}

func (s *SQLitePreferenceStore) GetAllAsMap(ctx context.Context, userID string) (map[string]string, error) {
	prefs, err := s.GetByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, p := range prefs {
		key := fmt.Sprintf("%s.%s", p.Category, p.Key)
		result[key] = p.Value
	}
	return result, nil
}
