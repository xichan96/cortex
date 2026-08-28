package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type SQLiteKnowledgeStore struct {
	db *sql.DB
}

func NewSQLiteKnowledgeStore(store *SQLiteStore) *SQLiteKnowledgeStore {
	return &SQLiteKnowledgeStore{db: store.DB()}
}

// DB 暴露底层 *sql.DB（Phase 2 全局锁/剪枝直接操作 DB 用）。
func (s *SQLiteKnowledgeStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteKnowledgeStore) Add(ctx context.Context, entry KnowledgeEntry) error {
	if entry.ID == "" {
		entry.ID = utils.NewID()
	}
	if entry.Category == "" {
		entry.Category = "project"
	}

	// 去重检查：查询是否有相同 normalized content 的记录
	normalized := normalizeContentForDedup(entry.Content)
	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM knowledge WHERE user_id = ? AND LOWER(REPLACE(REPLACE(REPLACE(REPLACE(content, ' ', ' '), '\t', ' '), '\n', ' '), '\r', ' ')) = ?`,
		entry.UserID, normalized).Scan(&existingID)
	if err == nil && existingID != "" {
		// 找到重复，更新 tags
		existingTags := s.getTagsByIDNoError(ctx, existingID)
		mergedTags := mergeTags(existingTags, entry.Tags)
		_, err = s.db.ExecContext(ctx,
			`UPDATE knowledge SET tags = ?, updated_at = ? WHERE id = ?`,
			joinTags(mergedTags), time.Now(), existingID)
		return err
	}
	if err != nil && err != sql.ErrNoRows {
		// 查询出错但不是 no rows，继续尝试插入
	}

	metadata, _ := safeJSONMarshal(entry.Metadata)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO knowledge (id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.UserID, entry.Category, entry.Content, joinTags(entry.Tags),
		string(metadata), entry.Source, entry.Priority, time.Now(), time.Now())
	return err
}

func (s *SQLiteKnowledgeStore) getTagsByIDNoError(ctx context.Context, id string) []string {
	var tagsStr string
	err := s.db.QueryRowContext(ctx, `SELECT tags FROM knowledge WHERE id = ?`, id).Scan(&tagsStr)
	if err != nil {
		return nil
	}
	return parseTagsFromDB(tagsStr)
}

func normalizeContentForDedup(content string) string {
	content = strings.TrimSpace(content)
	content = strings.ToLower(content)
	var b strings.Builder
	prevSpace := false
	for _, c := range content {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(c)
			prevSpace = false
		}
	}
	return b.String()
}

func mergeTags(existing, new []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, t := range existing {
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	for _, t := range new {
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	return result
}

func (s *SQLiteKnowledgeStore) Get(ctx context.Context, id string) (*KnowledgeEntry, error) {
	var entry KnowledgeEntry
	var tags, metadata string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at 
         FROM knowledge WHERE id = ?`, id).Scan(
		&entry.ID, &entry.UserID, &entry.Category, &entry.Content, &tags, &metadata,
		&entry.Source, &entry.Priority, &entry.CreatedAt, &entry.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry.Tags = parseTagsFromDB(tags)
	_ = safeJSONUnmarshal(metadata, &entry.Metadata)
	return &entry, nil
}

func (s *SQLiteKnowledgeStore) Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error) {
	query := `SELECT id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at, usage_count, last_usage
              FROM knowledge WHERE user_id = ?`
	args := []interface{}{userID}

	useScore := false
	if opts != nil && opts.Query != "" {
		// 关键词下推：content/tags/category 任一命中即可（LIKE %..% 不走索引，
		// 但 MaxKnowledge 默认 5000 量级 + LIMIT 下推后成本可控）。
		pattern := "%" + escapeLikePattern(strings.ToLower(opts.Query)) + "%"
		query += ` AND (LOWER(content) LIKE ? ESCAPE '\' OR LOWER(tags) LIKE ? ESCAPE '\' OR LOWER(category) LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
		useScore = true
	}
	if opts != nil && opts.Category != "" {
		query += " AND category = ?"
		args = append(args, opts.Category)
	}
	if opts != nil && opts.Priority != nil {
		query += " AND priority >= ?"
		args = append(args, int(*opts.Priority))
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MemoryItem
	usageMap := make(map[string]int)
	for rows.Next() {
		var entry KnowledgeEntry
		var tags, metadata string
		var usage int
		var lastUsage *time.Time
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.Category, &entry.Content, &tags, &metadata,
			&entry.Source, &entry.Priority, &entry.CreatedAt, &entry.UpdatedAt, &usage, &lastUsage); err != nil {
			return nil, err
		}
		entry.Tags = parseTagsFromDB(tags)
		_ = safeJSONUnmarshal(metadata, &entry.Metadata)
		if opts != nil && len(opts.Tags) > 0 {
			if !entryHasAllTags(entry.Tags, opts.Tags) {
				continue
			}
		}
		if opts != nil {
			if opts.Since != nil && entry.CreatedAt.Before(*opts.Since) {
				continue
			}
			if opts.Until != nil && entry.CreatedAt.After(*opts.Until) {
				continue
			}
		}
		usageMap[entry.ID] = usage
		items = append(items, MemoryItem{
			ID:        entry.ID,
			Type:      MemoryTypeKnowledge,
			Key:       entry.Category,
			Value:     entry.Content,
			Metadata:  entry.Metadata,
			Priority:  entry.Priority,
			Tags:      entry.Tags,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	queryText := ""
	if opts != nil {
		queryText = opts.Query
	}
	sort.Slice(items, func(i, j int) bool {
		if useScore && queryText != "" {
			si := utils.PageKeywordScore(queryText, items[i].Key, items[i].Value)
			sj := utils.PageKeywordScore(queryText, items[j].Key, items[j].Value)
			if si != sj {
				return si > sj
			}
		}
		ui, uj := usageMap[items[i].ID], usageMap[items[j].ID]
		if ui != uj {
			return ui > uj
		}
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

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

	q := ""
	if opts != nil {
		q = opts.Query
	}
	return &SearchResult{Items: items, Total: total, HasMore: hasMore, Query: q}, nil
}

// RecordKnowledgeUse 记录一条知识被实际引用：usage_count + 1 并刷新 last_usage。
// 仅由「模型实际引用」路径调用（见 dino/mem 引用反馈），不用于「返回即计数」。
func (s *SQLiteKnowledgeStore) RecordKnowledgeUse(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE knowledge SET usage_count = COALESCE(usage_count, 0) + 1, last_usage = ? WHERE id = ?`,
		time.Now(), id)
	return err
}

func entryHasAllTags(have []string, need []string) bool {
nextNeed:
	for _, t := range need {
		for _, h := range have {
			if h == t {
				continue nextNeed
			}
		}
		return false
	}
	return true
}

func matchKnowledgeQuery(entry *KnowledgeEntry, query string) bool {
	q := strings.ToLower(query)
	if strings.Contains(strings.ToLower(entry.Content), q) {
		return true
	}
	title, _ := entry.Metadata["title"].(string)
	if title != "" && strings.Contains(strings.ToLower(title), q) {
		return true
	}
	for _, tag := range entry.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

func (s *SQLiteKnowledgeStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM knowledge WHERE id = ?", id)
	return err
}

func (s *SQLiteKnowledgeStore) Clear(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM knowledge WHERE user_id = ?", userID)
	return err
}

func (s *SQLiteKnowledgeStore) GetStats(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM knowledge WHERE user_id = ?", userID).Scan(&count)
	return count, err
}

func (s *SQLiteKnowledgeStore) GetByTags(ctx context.Context, userID string, tags []string) ([]KnowledgeEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at 
         FROM knowledge WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []KnowledgeEntry
	for rows.Next() {
		var entry KnowledgeEntry
		var tagsStr, metadata string
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.Category, &entry.Content, &tagsStr, &metadata,
			&entry.Source, &entry.Priority, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entry.Tags = parseTagsFromDB(tagsStr)
		safeJSONUnmarshal(metadata, &entry.Metadata)
		hasTag := false
		for _, t1 := range entry.Tags {
			for _, t2 := range tags {
				if t1 == t2 {
					hasTag = true
					break
				}
			}
			if hasTag {
				break
			}
		}
		if hasTag {
			entries = append(entries, entry)
		}
	}
	return entries, rows.Err()
}

func (s *SQLiteKnowledgeStore) GetByCategory(ctx context.Context, userID, category string) ([]KnowledgeEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, category, content, tags, metadata, source, priority, created_at, updated_at 
         FROM knowledge WHERE user_id = ? AND category = ?`, userID, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []KnowledgeEntry
	for rows.Next() {
		var entry KnowledgeEntry
		var tags, metadata string
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.Category, &entry.Content, &tags, &metadata,
			&entry.Source, &entry.Priority, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entry.Tags = parseTagsFromDB(tags)
		safeJSONUnmarshal(metadata, &entry.Metadata)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLiteKnowledgeStore) Update(ctx context.Context, entry KnowledgeEntry) error {
	entry.UpdatedAt = time.Now()
	metadata, _ := safeJSONMarshal(entry.Metadata)
	_, err := s.db.ExecContext(ctx,
		`UPDATE knowledge SET content = ?, tags = ?, metadata = ?, priority = ?, updated_at = ?
         WHERE id = ?`,
		entry.Content, joinTags(entry.Tags), string(metadata), entry.Priority, entry.UpdatedAt, entry.ID)
	return err
}
