package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type SQLiteIndexStore struct {
	db *sql.DB
}

func NewSQLiteIndexStore(store *SQLiteStore) *SQLiteIndexStore {
	return &SQLiteIndexStore{db: store.DB()}
}

func (s *SQLiteIndexStore) CreateIndex(ctx context.Context, userID, sourceID, title string, nodes []*IndexNode) (*IndexTree, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	indexID := utils.NewID()
	rootID := utils.NewID()
	now := time.Now()

	nodeMap := make(map[string]*IndexNode)
	for _, node := range nodes {
		if node.ID == "" {
			node.ID = utils.NewID()
		}
		node.CreatedAt = now
		node.UpdatedAt = now
		nodeMap[node.ID] = node
	}
	for _, node := range nodes {
		if node.ParentID == "" {
			rootID = node.ID
			break
		}
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO indexes (id, user_id, source_id, title, root_id, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		indexID, userID, sourceID, title, rootID, now, now)
	if err != nil {
		return nil, err
	}

	for _, node := range nodes {
		meta, _ := safeJSONMarshal(node.Metadata)
		_, err = tx.ExecContext(ctx,
			`INSERT INTO index_nodes (id, index_id, parent_id, title, level, start_line, end_line, content, summary, prefix_summary, tags, metadata, created_at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			node.ID, indexID, coalesceString(node.ParentID, ""), node.Title, node.Level,
			node.StartLine, node.EndLine, coalesceString(node.Content, ""),
			coalesceString(node.Summary, ""), coalesceString(node.PrefixSummary, ""), joinTags(node.Tags), string(meta), now, now)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &IndexTree{
		ID:        indexID,
		RootID:    rootID,
		UserID:    userID,
		SourceID:  sourceID,
		Title:     title,
		Nodes:     nodeMap,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (s *SQLiteIndexStore) GetIndex(ctx context.Context, userID, sourceID string) (*IndexTree, error) {
	var idxID, rootID, title string
	var createdAt, updatedAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT id, root_id, title, created_at, updated_at FROM indexes WHERE user_id = ? AND source_id = ?`,
		userID, sourceID).Scan(&idxID, &rootID, &title, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, parent_id, title, level, start_line, end_line, content, summary, prefix_summary, tags, metadata, created_at, updated_at 
         FROM index_nodes WHERE index_id = ? ORDER BY start_line ASC, id ASC`, idxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*IndexNode
	for rows.Next() {
		node := &IndexNode{}
		var parentID, content, summary, prefixSummary, tags, metadata string
		if err := rows.Scan(&node.ID, &parentID, &node.Title, &node.Level,
			&node.StartLine, &node.EndLine, &content, &summary, &prefixSummary, &tags, &metadata,
			&node.CreatedAt, &node.UpdatedAt); err != nil {
			return nil, err
		}
		node.ParentID = parentID
		node.Content = content
		node.Summary = summary
		node.PrefixSummary = prefixSummary
		node.Tags = parseTagsFromDB(tags)
		_ = safeJSONUnmarshal(metadata, &node.Metadata)
		list = append(list, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].StartLine != list[j].StartLine {
			return list[i].StartLine < list[j].StartLine
		}
		return list[i].ID < list[j].ID
	})

	nodeMap := make(map[string]*IndexNode)
	for _, p := range list {
		nodeMap[p.ID] = p
	}
	for _, p := range list {
		if p.ParentID != "" {
			if par, ok := nodeMap[p.ParentID]; ok {
				par.Nodes = append(par.Nodes, p)
			}
		}
	}
	for _, p := range list {
		if p.Nodes == nil {
			p.Nodes = []*IndexNode{}
		}
	}

	return &IndexTree{
		ID:        idxID,
		RootID:    rootID,
		UserID:    userID,
		SourceID:  sourceID,
		Title:     title,
		Nodes:     nodeMap,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func (s *SQLiteIndexStore) DeleteIndex(ctx context.Context, userID, sourceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var idxID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM indexes WHERE user_id = ? AND source_id = ?`, userID, sourceID).Scan(&idxID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM index_nodes WHERE index_id = ?", idxID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM indexes WHERE user_id = ? AND source_id = ?", userID, sourceID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *SQLiteIndexStore) SearchIndex(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	likePat := "%" + escapeLikePattern(query) + "%"
	where := `i.user_id = ? AND (n.title LIKE ? ESCAPE '\' OR n.content LIKE ? ESCAPE '\' OR n.summary LIKE ? ESCAPE '\' OR IFNULL(n.prefix_summary, '') LIKE ? ESCAPE '\')`
	args := []interface{}{userID, likePat, likePat, likePat, likePat}

	var total int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM index_nodes n JOIN indexes i ON n.index_id = i.id WHERE `+where,
		args...).Scan(&total)
	if err != nil {
		return nil, err
	}

	q := `SELECT n.id, n.parent_id, n.title, n.level, n.start_line, n.end_line, n.content, n.summary, n.prefix_summary, n.tags, n.metadata, n.created_at, n.updated_at 
         FROM index_nodes n
         JOIN indexes i ON n.index_id = i.id
         WHERE ` + where + ` ORDER BY n.level ASC, (CASE WHEN n.title LIKE ? ESCAPE '\' THEN 0 ELSE 1 END) ASC, n.title ASC LIMIT ?`
	args2 := append(append(append([]interface{}{}, args...), likePat), limit)
	rows, err := s.db.QueryContext(ctx, q, args2...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*IndexNode
	for rows.Next() {
		var node IndexNode
		var parentID, content, summary, prefixSummary, tags, metadata string
		if err := rows.Scan(&node.ID, &parentID, &node.Title, &node.Level,
			&node.StartLine, &node.EndLine, &content, &summary, &prefixSummary, &tags, &metadata,
			&node.CreatedAt, &node.UpdatedAt); err != nil {
			return nil, err
		}
		node.ParentID = parentID
		node.Content = content
		node.Summary = summary
		node.PrefixSummary = prefixSummary
		node.Tags = parseTagsFromDB(tags)
		_ = safeJSONUnmarshal(metadata, &node.Metadata)
		nodes = append(nodes, &node)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &IndexSearchResult{Nodes: nodes, Total: total, Query: query}, nil
}

func (s *SQLiteIndexStore) GetAllIndexes(ctx context.Context, userID string) ([]*IndexTree, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, source_id FROM indexes WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trees []*IndexTree
	for rows.Next() {
		var idxID, sourceID string
		if err := rows.Scan(&idxID, &sourceID); err != nil {
			return nil, err
		}
		tree, err := s.GetIndex(ctx, userID, sourceID)
		if err != nil {
			return nil, err
		}
		if tree != nil {
			trees = append(trees, tree)
		}
	}
	return trees, rows.Err()
}

func (s *SQLiteIndexStore) UpdateIndex(ctx context.Context, tree *IndexTree) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE indexes SET title = ?, updated_at = ? WHERE user_id = ? AND source_id = ?`,
		tree.Title, time.Now(), tree.UserID, tree.SourceID)
	return err
}

func (s *SQLiteIndexStore) AddNode(ctx context.Context, userID, sourceID string, node *IndexNode, parentID string) error {
	idx, err := s.GetIndex(ctx, userID, sourceID)
	if err != nil || idx == nil {
		return fmt.Errorf("index not found")
	}

	node.ID = coalesceString(node.ID, utils.NewID())
	node.ParentID = parentID
	node.CreatedAt = time.Now()
	node.UpdatedAt = time.Now()

	meta, _ := safeJSONMarshal(node.Metadata)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO index_nodes (id, index_id, parent_id, title, level, start_line, end_line, content, summary, prefix_summary, tags, metadata, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, idx.ID, coalesceString(parentID, ""), node.Title, node.Level,
		node.StartLine, node.EndLine, coalesceString(node.Content, ""),
		coalesceString(node.Summary, ""), coalesceString(node.PrefixSummary, ""), joinTags(node.Tags), string(meta), node.CreatedAt, node.UpdatedAt)

	return err
}

func (s *SQLiteIndexStore) RemoveNode(ctx context.Context, userID, sourceID, nodeID string) error {
	var idxID string
	err := s.db.QueryRowContext(ctx,
		`SELECT n.index_id FROM index_nodes n
		 JOIN indexes i ON n.index_id = i.id
		 WHERE i.user_id = ? AND i.source_id = ? AND n.id = ?`,
		userID, sourceID, nodeID).Scan(&idxID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("node not found")
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`WITH RECURSIVE sub(id) AS (
			SELECT ? AS id
			UNION ALL
			SELECT n.id FROM index_nodes n INNER JOIN sub ON n.parent_id = sub.id WHERE n.index_id = ?
		)
		DELETE FROM index_nodes WHERE id IN (SELECT id FROM sub) AND index_id = ?`,
		nodeID, idxID, idxID)
	return err
}

func (s *SQLiteIndexStore) UpdateNode(ctx context.Context, userID, sourceID string, node *IndexNode) error {
	node.UpdatedAt = time.Now()
	meta, _ := safeJSONMarshal(node.Metadata)
	res, err := s.db.ExecContext(ctx,
		`UPDATE index_nodes SET title = ?, content = ?, summary = ?, prefix_summary = ?, tags = ?, metadata = ?, updated_at = ?
		 WHERE id = ? AND index_id = (SELECT id FROM indexes WHERE user_id = ? AND source_id = ? LIMIT 1)`,
		node.Title, coalesceString(node.Content, ""), coalesceString(node.Summary, ""),
		coalesceString(node.PrefixSummary, ""), joinTags(node.Tags), string(meta), node.UpdatedAt,
		node.ID, userID, sourceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("node not found")
	}
	return nil
}
