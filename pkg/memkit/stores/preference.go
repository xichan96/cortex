package stores

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type InMemoryPreferenceStore struct {
	mu          sync.RWMutex
	preferences map[string]map[string]*Preference
}

func NewInMemoryPreferenceStore() *InMemoryPreferenceStore {
	return &InMemoryPreferenceStore{
		preferences: make(map[string]map[string]*Preference),
	}
}

func (s *InMemoryPreferenceStore) makeKey(userID, category, key string) string {
	return fmt.Sprintf("%s:%s:%s", userID, category, key)
}

func (s *InMemoryPreferenceStore) Set(ctx context.Context, pref Preference) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pref.UserID == "" {
		return fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	}
	if pref.Key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidInput)
	}
	if pref.Category == "" {
		pref.Category = "default"
	}

	now := time.Now()
	if pref.CreatedAt.IsZero() {
		pref.CreatedAt = now
	}
	pref.UpdatedAt = now

	if pref.ID == "" {
		pref.ID = uuid.New().String()
	}

	key := s.makeKey(pref.UserID, pref.Category, pref.Key)
	if s.preferences[pref.UserID] == nil {
		s.preferences[pref.UserID] = make(map[string]*Preference)
	}
	s.preferences[pref.UserID][key] = &pref

	return nil
}

func (s *InMemoryPreferenceStore) Get(ctx context.Context, userID, category, key string) (*Preference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if category == "" {
		category = "default"
	}
	key = s.makeKey(userID, category, key)

	if s.preferences[userID] == nil {
		return nil, nil
	}

	pref, ok := s.preferences[userID][key]
	if !ok {
		return nil, nil
	}

	result := *pref
	return &result, nil
}

func (s *InMemoryPreferenceStore) GetByUser(ctx context.Context, userID string) ([]Preference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Preference
	if s.preferences[userID] != nil {
		for _, pref := range s.preferences[userID] {
			result = append(result, *pref)
		}
	}
	return result, nil
}

func (s *InMemoryPreferenceStore) GetByCategory(ctx context.Context, userID, category string) ([]Preference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if category == "" {
		category = "default"
	}

	var result []Preference
	prefix := fmt.Sprintf("%s:%s:", userID, category)
	if s.preferences[userID] != nil {
		for key, pref := range s.preferences[userID] {
			if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
				result = append(result, *pref)
			}
		}
	}
	return result, nil
}

func (s *InMemoryPreferenceStore) Delete(ctx context.Context, userID, category, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if category == "" {
		category = "default"
	}
	key = s.makeKey(userID, category, key)

	if s.preferences[userID] != nil {
		delete(s.preferences[userID], key)
	}
	return nil
}

func (s *InMemoryPreferenceStore) Clear(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.preferences, userID)
	return nil
}

func (s *InMemoryPreferenceStore) Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []MemoryItem
	if s.preferences[userID] == nil {
		return &SearchResult{Items: items, Total: 0}, nil
	}

	prefix := ""
	if opts != nil && opts.Category != "" {
		prefix = fmt.Sprintf("%s:%s:", userID, opts.Category)
	} else {
		prefix = fmt.Sprintf("%s:", userID)
	}

	for key, pref := range s.preferences[userID] {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			continue
		}

		item := MemoryItem{
			ID:        pref.ID,
			Type:      MemoryTypePreference,
			Key:       pref.Key,
			Value:     pref.Value,
			Priority:  pref.Priority,
			Metadata:  stringMapToInterface(pref.Metadata),
			CreatedAt: pref.CreatedAt,
			UpdatedAt: pref.UpdatedAt,
		}

		if opts != nil {
			if opts.Priority != nil && pref.Priority < *opts.Priority {
				continue
			}
			if len(opts.Tags) > 0 && !hasIntersection(pref.Metadata["tags"], opts.Tags) {
				continue
			}
		}

		items = append(items, item)
	}

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
	}, nil
}

func (s *InMemoryPreferenceStore) GetAllAsMap(ctx context.Context, userID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string)
	if s.preferences[userID] != nil {
		for _, pref := range s.preferences[userID] {
			fullKey := fmt.Sprintf("%s.%s", pref.Category, pref.Key)
			result[fullKey] = pref.Value
		}
	}
	return result, nil
}

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

func hasIntersection(tagValue interface{}, tags []string) bool {
	if tagValue == nil {
		return false
	}
	tagStr, ok := tagValue.(string)
	if !ok {
		return false
	}
	for _, t := range tags {
		if tagStr == t {
			return true
		}
	}
	return false
}
