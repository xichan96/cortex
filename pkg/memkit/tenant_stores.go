package memkit

import (
	"context"
	"fmt"
)

type tenantPreferenceStore struct {
	inner PreferenceStore
	uid   string
}

func (t *tenantPreferenceStore) Set(ctx context.Context, pref Preference) error {
	pref.UserID = t.uid
	return t.inner.Set(ctx, pref)
}

func (t *tenantPreferenceStore) Get(ctx context.Context, _, category, key string) (*Preference, error) {
	return t.inner.Get(ctx, t.uid, category, key)
}

func (t *tenantPreferenceStore) GetByUser(ctx context.Context, _ string) ([]Preference, error) {
	return t.inner.GetByUser(ctx, t.uid)
}

func (t *tenantPreferenceStore) GetByCategory(ctx context.Context, _, category string) ([]Preference, error) {
	return t.inner.GetByCategory(ctx, t.uid, category)
}

func (t *tenantPreferenceStore) Delete(ctx context.Context, _, category, key string) error {
	return t.inner.Delete(ctx, t.uid, category, key)
}

func (t *tenantPreferenceStore) Clear(ctx context.Context, _ string) error {
	return t.inner.Clear(ctx, t.uid)
}

func (t *tenantPreferenceStore) Search(ctx context.Context, _ string, opts *SearchOptions) (*SearchResult, error) {
	return t.inner.Search(ctx, t.uid, opts)
}

func (t *tenantPreferenceStore) GetAllAsMap(ctx context.Context, _ string) (map[string]string, error) {
	return t.inner.GetAllAsMap(ctx, t.uid)
}

type tenantKnowledgeStore struct {
	inner KnowledgeStore
	uid   string
}

func (t *tenantKnowledgeStore) Add(ctx context.Context, entry KnowledgeEntry) error {
	entry.UserID = t.uid
	return t.inner.Add(ctx, entry)
}

func (t *tenantKnowledgeStore) Update(ctx context.Context, entry KnowledgeEntry) error {
	existing, err := t.inner.Get(ctx, entry.ID)
	if err != nil {
		return err
	}
	if existing == nil || existing.UserID != t.uid {
		return fmt.Errorf("%w: knowledge entry", ErrNotFound)
	}
	entry.UserID = t.uid
	return t.inner.Update(ctx, entry)
}

func (t *tenantKnowledgeStore) Get(ctx context.Context, id string) (*KnowledgeEntry, error) {
	e, err := t.inner.Get(ctx, id)
	if err != nil || e == nil {
		return e, err
	}
	if e.UserID != t.uid {
		return nil, fmt.Errorf("%w", ErrNotFound)
	}
	return e, nil
}

func (t *tenantKnowledgeStore) Delete(ctx context.Context, id string) error {
	e, err := t.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	if e == nil || e.UserID != t.uid {
		return nil
	}
	return t.inner.Delete(ctx, id)
}

func (t *tenantKnowledgeStore) Search(ctx context.Context, _ string, opts *SearchOptions) (*SearchResult, error) {
	return t.inner.Search(ctx, t.uid, opts)
}

func (t *tenantKnowledgeStore) GetByTags(ctx context.Context, _ string, tags []string) ([]KnowledgeEntry, error) {
	return t.inner.GetByTags(ctx, t.uid, tags)
}

func (t *tenantKnowledgeStore) GetByCategory(ctx context.Context, _, category string) ([]KnowledgeEntry, error) {
	return t.inner.GetByCategory(ctx, t.uid, category)
}

func (t *tenantKnowledgeStore) Clear(ctx context.Context, _ string) error {
	return t.inner.Clear(ctx, t.uid)
}

func (t *tenantKnowledgeStore) GetStats(ctx context.Context, _ string) (int, error) {
	return t.inner.GetStats(ctx, t.uid)
}

func (t *tenantKnowledgeStore) RecordKnowledgeUse(ctx context.Context, id string) error {
	e, err := t.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	if e == nil || e.UserID != t.uid {
		return nil
	}
	return t.inner.RecordKnowledgeUse(ctx, id)
}

type tenantIndexStore struct {
	inner IndexStore
	uid   string
}

func (t *tenantIndexStore) CreateIndex(ctx context.Context, _, sourceID, title string, nodes []*IndexNode) (*IndexTree, error) {
	return t.inner.CreateIndex(ctx, t.uid, sourceID, title, nodes)
}

func (t *tenantIndexStore) GetIndex(ctx context.Context, _, sourceID string) (*IndexTree, error) {
	return t.inner.GetIndex(ctx, t.uid, sourceID)
}

func (t *tenantIndexStore) UpdateIndex(ctx context.Context, tree *IndexTree) error {
	if tree.UserID != t.uid {
		return fmt.Errorf("index not found")
	}
	return t.inner.UpdateIndex(ctx, tree)
}

func (t *tenantIndexStore) DeleteIndex(ctx context.Context, _, sourceID string) error {
	return t.inner.DeleteIndex(ctx, t.uid, sourceID)
}

func (t *tenantIndexStore) SearchIndex(ctx context.Context, _ string, query string, limit int) (*IndexSearchResult, error) {
	return t.inner.SearchIndex(ctx, t.uid, query, limit)
}

func (t *tenantIndexStore) GetAllIndexes(ctx context.Context, _ string) ([]*IndexTree, error) {
	return t.inner.GetAllIndexes(ctx, t.uid)
}

func (t *tenantIndexStore) AddNode(ctx context.Context, _, sourceID string, node *IndexNode, parentID string) error {
	return t.inner.AddNode(ctx, t.uid, sourceID, node, parentID)
}

func (t *tenantIndexStore) RemoveNode(ctx context.Context, _, sourceID, nodeID string) error {
	return t.inner.RemoveNode(ctx, t.uid, sourceID, nodeID)
}

func (t *tenantIndexStore) UpdateNode(ctx context.Context, _, sourceID string, node *IndexNode) error {
	return t.inner.UpdateNode(ctx, t.uid, sourceID, node)
}

type tenantPageIndexStore struct {
	inner PageIndexStore
	uid   string
}

func (t *tenantPageIndexStore) Upsert(ctx context.Context, doc PageIndexDoc) error {
	doc.UserID = t.uid
	return t.inner.Upsert(ctx, doc)
}

func (t *tenantPageIndexStore) Delete(ctx context.Context, _, id string) error {
	return t.inner.Delete(ctx, t.uid, id)
}

func (t *tenantPageIndexStore) DeleteByUser(ctx context.Context, _ string) error {
	return t.inner.DeleteByUser(ctx, t.uid)
}

func (t *tenantPageIndexStore) DeleteByKinds(ctx context.Context, _ string, kinds []PageIndexKind) error {
	return t.inner.DeleteByKinds(ctx, t.uid, kinds)
}

func (t *tenantPageIndexStore) CountByUser(ctx context.Context, _ string) (int, error) {
	return t.inner.CountByUser(ctx, t.uid)
}

func (t *tenantPageIndexStore) Search(ctx context.Context, _, query string, opts *PageIndexSearchOptions) ([]PageIndexHit, error) {
	return t.inner.Search(ctx, t.uid, query, opts)
}
