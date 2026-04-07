package stores

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/xichan96/cortex/pkg/memkit/utils"
)

func pageIndexKindStrings(kinds []PageIndexKind) []string {
	if len(kinds) == 0 {
		return nil
	}
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

type InMemoryPageIndexStore struct {
	mu   sync.RWMutex
	byID map[string]*PageIndexDoc
}

func NewInMemoryPageIndexStore() *InMemoryPageIndexStore {
	return &InMemoryPageIndexStore{
		byID: make(map[string]*PageIndexDoc),
	}
}

func (s *InMemoryPageIndexStore) Upsert(ctx context.Context, doc PageIndexDoc) error {
	_ = ctx
	if doc.UserID == "" {
		return fmt.Errorf("memory: page_index user_id required")
	}
	if doc.Text == "" && doc.Title == "" {
		return fmt.Errorf("memory: page_index title or text required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	d := doc
	s.byID[d.ID] = &d
	return nil
}

func (s *InMemoryPageIndexStore) Delete(ctx context.Context, userID, id string) error {
	_ = ctx
	if userID == "" {
		return fmt.Errorf("memory: page_index user_id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok || d.UserID != userID {
		return nil
	}
	delete(s.byID, id)
	return nil
}

func (s *InMemoryPageIndexStore) DeleteByUser(ctx context.Context, userID string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, d := range s.byID {
		if d.UserID == userID {
			delete(s.byID, id)
		}
	}
	return nil
}

func (s *InMemoryPageIndexStore) DeleteByKinds(ctx context.Context, userID string, kinds []PageIndexKind) error {
	_ = ctx
	if len(kinds) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, d := range s.byID {
		if d.UserID != userID {
			continue
		}
		for _, k := range kinds {
			if d.Kind == k {
				delete(s.byID, id)
				break
			}
		}
	}
	return nil
}

func (s *InMemoryPageIndexStore) CountByUser(ctx context.Context, userID string) (int, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, d := range s.byID {
		if d.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (s *InMemoryPageIndexStore) Search(ctx context.Context, userID, query string, opts *PageIndexSearchOptions) ([]PageIndexHit, error) {
	_ = ctx
	limit := 12
	minScore := 0.0
	var kindStrs []string
	if opts != nil {
		if opts.Limit > 0 {
			limit = opts.Limit
		}
		minScore = opts.MinScore
		kindStrs = pageIndexKindStrings(opts.Kinds)
	}
	s.mu.RLock()
	var hits []PageIndexHit
	for _, d := range s.byID {
		if d.UserID != userID {
			continue
		}
		if !utils.KindAllowed(string(d.Kind), kindStrs) {
			continue
		}
		sc := utils.PageKeywordScore(query, d.Title, d.Text)
		if sc < minScore {
			continue
		}
		hits = append(hits, PageIndexHit{Doc: *d, Score: sc})
	}
	s.mu.RUnlock()
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
