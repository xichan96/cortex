package stores

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type InMemoryKnowledgeStore struct {
	mu      sync.RWMutex
	entries map[string]*KnowledgeEntry
	indexes map[string][]string
}

func NewInMemoryKnowledgeStore() *InMemoryKnowledgeStore {
	return &InMemoryKnowledgeStore{
		entries: make(map[string]*KnowledgeEntry),
		indexes: make(map[string][]string),
	}
}

func (s *InMemoryKnowledgeStore) Add(ctx context.Context, entry KnowledgeEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	if entry.Content == "" {
		return fmt.Errorf("content is required")
	}

	// 去重检查：规范化后精确匹配
	normalized := normalizeContent(entry.Content)
	userKey := fmt.Sprintf("user:%s", entry.UserID)
	userIDs, ok := s.indexes[userKey]
	if ok {
		for _, id := range userIDs {
			if existing, exists := s.entries[id]; exists {
				if normalizeContent(existing.Content) == normalized {
					// 找到重复，合并 tags
					existing.Tags = unionStrings(existing.Tags, entry.Tags)
					existing.UpdatedAt = time.Now()
					return nil
				}
			}
		}
	}

	now := time.Now()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Category == "" {
		entry.Category = "project"
	}

	s.entries[entry.ID] = &entry
	s.rebuildIndexes(&entry)

	return nil
}

// normalizeContent 规范化内容用于去重比较
func normalizeContent(content string) string {
	// trim + 折叠空白为单个空格 + 小写
	content = strings.TrimSpace(content)
	content = strings.ToLower(content)
	// 折叠连续空白
	content = collapseWhitespace(content)
	return content
}

// collapseWhitespace 将连续空白替换为单个空格
func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, c := range s {
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

// unionStrings 合并两个字符串切片，去重
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func (s *InMemoryKnowledgeStore) Update(ctx context.Context, entry KnowledgeEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.ID == "" {
		return fmt.Errorf("id is required for update")
	}

	existing, ok := s.entries[entry.ID]
	if !ok {
		return fmt.Errorf("knowledge entry not found: %s", entry.ID)
	}

	s.removeIndexes(existing)
	entry.CreatedAt = existing.CreatedAt
	entry.UpdatedAt = time.Now()
	s.entries[entry.ID] = &entry
	s.rebuildIndexes(&entry)

	return nil
}

func (s *InMemoryKnowledgeStore) Get(ctx context.Context, id string) (*KnowledgeEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[id]
	if !ok {
		return nil, nil
	}

	result := *entry
	return &result, nil
}

func (s *InMemoryKnowledgeStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if ok {
		s.removeIndexes(entry)
	}
	delete(s.entries, id)

	return nil
}

func (s *InMemoryKnowledgeStore) Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []MemoryItem
	var matchedIDs []string

	userKey := fmt.Sprintf("user:%s", userID)
	userIDs, ok := s.indexes[userKey]
	if !ok {
		return &SearchResult{Items: items, Total: 0}, nil
	}
	matchedIDs = userIDs

	if opts != nil {
		if opts.Category != "" {
			catKey := fmt.Sprintf("user:%s:category:%s", userID, opts.Category)
			if ids, ok := s.indexes[catKey]; ok {
				matchedIDs = intersectIDs(matchedIDs, ids)
			} else {
				matchedIDs = nil
			}
		}

		if len(opts.Tags) > 0 {
			var tagIDs []string
			for _, tag := range opts.Tags {
				tagKey := fmt.Sprintf("user:%s:tag:%s", userID, tag)
				if ids, ok := s.indexes[tagKey]; ok {
					tagIDs = append(tagIDs, ids...)
				}
			}
			matchedIDs = intersectIDs(matchedIDs, tagIDs)
		}

		if opts.Priority != nil {
			var priorityIDs []string
			for _, id := range matchedIDs {
				if entry, ok := s.entries[id]; ok && entry.Priority >= *opts.Priority {
					priorityIDs = append(priorityIDs, id)
				}
			}
			matchedIDs = priorityIDs
		}
	}

	for _, id := range matchedIDs {
		if entry, ok := s.entries[id]; ok {
			if opts != nil && opts.Query != "" {
				if !s.matchQuery(entry, opts.Query) {
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

			item := MemoryItem{
				ID:        entry.ID,
				Type:      MemoryTypeKnowledge,
				Key:       entry.Category,
				Value:     entry.Content,
				Metadata:  entry.Metadata,
				Priority:  entry.Priority,
				Tags:      entry.Tags,
				CreatedAt: entry.CreatedAt,
				UpdatedAt: entry.UpdatedAt,
			}
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	total := len(items)
	offset := 0
	limit := 50
	if opts != nil {
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		if opts.Offset > 0 {
			offset = opts.Offset
		}
	}

	hasMore := false
	if offset+limit < total {
		hasMore = true
	}
	if offset >= total {
		items = []MemoryItem{}
	} else if offset+limit < total {
		items = items[offset : offset+limit]
	} else {
		items = items[offset:]
	}

	return &SearchResult{
		Items:   items,
		Total:   total,
		HasMore: hasMore,
		Query:   opts.Query,
	}, nil
}

func (s *InMemoryKnowledgeStore) GetByTags(ctx context.Context, userID string, tags []string) ([]KnowledgeEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []KnowledgeEntry

	for _, tag := range tags {
		tagKey := fmt.Sprintf("user:%s:tag:%s", userID, tag)
		ids, ok := s.indexes[tagKey]
		if !ok {
			continue
		}
		for _, id := range ids {
			if entry, ok := s.entries[id]; ok {
				result = append(result, *entry)
			}
		}
	}

	return deduplicateEntries(result), nil
}

func (s *InMemoryKnowledgeStore) GetByCategory(ctx context.Context, userID, category string) ([]KnowledgeEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []KnowledgeEntry

	catKey := fmt.Sprintf("user:%s:category:%s", userID, category)
	ids, ok := s.indexes[catKey]
	if !ok {
		return result, nil
	}

	for _, id := range ids {
		if entry, ok := s.entries[id]; ok {
			result = append(result, *entry)
		}
	}

	return result, nil
}

func (s *InMemoryKnowledgeStore) Clear(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	userKey := fmt.Sprintf("user:%s", userID)
	ids, ok := s.indexes[userKey]
	if ok {
		for _, id := range ids {
			delete(s.entries, id)
		}
	}

	delete(s.indexes, userKey)
	pattern := fmt.Sprintf("user:%s:", userID)
	for key := range s.indexes {
		if strings.HasPrefix(key, pattern) {
			delete(s.indexes, key)
		}
	}

	return nil
}

func (s *InMemoryKnowledgeStore) GetStats(ctx context.Context, userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	userKey := fmt.Sprintf("user:%s", userID)
	ids, ok := s.indexes[userKey]
	if !ok {
		return 0, nil
	}
	return len(ids), nil
}

// RecordKnowledgeUse 记录一条知识被引用（内存实现：跟踪 usage 次数，
// 用于排序与剪枝豁免的语义一致性）。
func (s *InMemoryKnowledgeStore) RecordKnowledgeUse(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return nil
	}
	usage := 0
	if v, ok := entry.Metadata["usage_count"].(float64); ok {
		usage = int(v)
	}
	usage++
	if entry.Metadata == nil {
		entry.Metadata = make(map[string]interface{})
	}
	entry.Metadata["usage_count"] = usage
	return nil
}

func (s *InMemoryKnowledgeStore) rebuildIndexes(entry *KnowledgeEntry) {
	userKey := fmt.Sprintf("user:%s", entry.UserID)
	s.indexes[userKey] = append(s.indexes[userKey], entry.ID)

	catKey := fmt.Sprintf("user:%s:category:%s", entry.UserID, entry.Category)
	s.indexes[catKey] = append(s.indexes[catKey], entry.ID)

	for _, tag := range entry.Tags {
		tagKey := fmt.Sprintf("user:%s:tag:%s", entry.UserID, tag)
		s.indexes[tagKey] = append(s.indexes[tagKey], entry.ID)
	}
}

func (s *InMemoryKnowledgeStore) removeIndexes(entry *KnowledgeEntry) {
	userKey := fmt.Sprintf("user:%s", entry.UserID)
	s.indexes[userKey] = removeFromSlice(s.indexes[userKey], entry.ID)

	catKey := fmt.Sprintf("user:%s:category:%s", entry.UserID, entry.Category)
	s.indexes[catKey] = removeFromSlice(s.indexes[catKey], entry.ID)

	for _, tag := range entry.Tags {
		tagKey := fmt.Sprintf("user:%s:tag:%s", entry.UserID, tag)
		s.indexes[tagKey] = removeFromSlice(s.indexes[tagKey], entry.ID)
	}
}

func (s *InMemoryKnowledgeStore) matchQuery(entry *KnowledgeEntry, query string) bool {
	query = strings.ToLower(query)
	content := strings.ToLower(entry.Content)
	if strings.Contains(content, query) {
		return true
	}
	title, _ := entry.Metadata["title"].(string)
	if title != "" && strings.Contains(strings.ToLower(title), query) {
		return true
	}
	for _, tag := range entry.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

func intersectIDs(a, b []string) []string {
	set := make(map[string]bool)
	for _, id := range a {
		set[id] = true
	}
	var result []string
	for _, id := range b {
		if set[id] {
			result = append(result, id)
		}
	}
	return result
}

func removeFromSlice(slice []string, item string) []string {
	var result []string
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func deduplicateEntries(entries []KnowledgeEntry) []KnowledgeEntry {
	seen := make(map[string]bool)
	var result []KnowledgeEntry
	for _, e := range entries {
		if !seen[e.ID] {
			seen[e.ID] = true
			result = append(result, e)
		}
	}
	return result
}
