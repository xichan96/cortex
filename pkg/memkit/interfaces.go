package memkit

import "context"

// PreferenceStore Manage user preferences.
//
// Error:
//   - ErrInvalidInput: userID, category, key is invalid
type PreferenceStore interface {
	Set(ctx context.Context, pref Preference) error
	Get(ctx context.Context, userID, category, key string) (*Preference, error)
	GetByUser(ctx context.Context, userID string) ([]Preference, error)
	GetByCategory(ctx context.Context, userID, category string) ([]Preference, error)
	Delete(ctx context.Context, userID, category, key string) error
	Clear(ctx context.Context, userID string) error
	Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error)
	GetAllAsMap(ctx context.Context, userID string) (map[string]string, error)
}

// KnowledgeStore Manage user knowledge base.
//
// Error:
//   - ErrInvalidInput: userID is empty or content is empty
type KnowledgeStore interface {
	// Add add knowledge entry
	Add(ctx context.Context, entry KnowledgeEntry) error
	// Update update knowledge entry
	Update(ctx context.Context, entry KnowledgeEntry) error
	// Get get knowledge entry
	Get(ctx context.Context, id string) (*KnowledgeEntry, error)
	// Delete delete knowledge entry
	Delete(ctx context.Context, id string) error
	// Search search knowledge
	Search(ctx context.Context, userID string, opts *SearchOptions) (*SearchResult, error)
	// GetByTags get knowledge by tags
	GetByTags(ctx context.Context, userID string, tags []string) ([]KnowledgeEntry, error)
	// GetByCategory 按分类获取知识
	GetByCategory(ctx context.Context, userID, category string) ([]KnowledgeEntry, error)
	// Clear 清除用户所有知识
	Clear(ctx context.Context, userID string) error
	// GetStats 获取知识条目数量
	GetStats(ctx context.Context, userID string) (int, error)
}

// IndexStore Manage Markdown index.
//
// Error:
//   - ErrInvalidInput: userID or sourceID is empty
type IndexStore interface {
	// CreateIndex create index
	CreateIndex(ctx context.Context, userID, sourceID, title string, nodes []*IndexNode) (*IndexTree, error)
	// GetIndex get index
	GetIndex(ctx context.Context, userID, sourceID string) (*IndexTree, error)
	// UpdateIndex update index
	UpdateIndex(ctx context.Context, tree *IndexTree) error
	// DeleteIndex delete index
	DeleteIndex(ctx context.Context, userID, sourceID string) error
	// SearchIndex search index nodes
	SearchIndex(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error)
	// GetAllIndexes get all user indexes
	GetAllIndexes(ctx context.Context, userID string) ([]*IndexTree, error)
	// AddNode add node
	AddNode(ctx context.Context, userID, sourceID string, node *IndexNode, parentID string) error
	// RemoveNode delete node
	RemoveNode(ctx context.Context, userID, sourceID, nodeID string) error
	// UpdateNode update node
	UpdateNode(ctx context.Context, userID, sourceID string, node *IndexNode) error
}

// PageIndexStore Manage and search text documents, for RAG scenarios.
//
// Error:
//   - ErrInvalidInput: userID is empty and title/text are empty
type PageIndexStore interface {
	// Upsert insert or update document
	Upsert(ctx context.Context, doc PageIndexDoc) error
	// Delete delete document (only delete if id belongs to userID)
	Delete(ctx context.Context, userID, id string) error
	// DeleteByUser delete all user documents
	DeleteByUser(ctx context.Context, userID string) error
	// DeleteByKinds delete documents by type
	DeleteByKinds(ctx context.Context, userID string, kinds []PageIndexKind) error
	// CountByUser count documents
	CountByUser(ctx context.Context, userID string) (int, error)
	// Search search documents
	Search(ctx context.Context, userID, query string, opts *PageIndexSearchOptions) ([]PageIndexHit, error)
}
