package typescore

import (
	"context"
	"time"
)

type LLMProvider interface {
	Chat(ctx context.Context, messages []Message) (Message, error)
	ChatWithTools(ctx context.Context, messages []Message, tools []Tool) (Message, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type MemoryType string

const (
	MemoryTypePreference MemoryType = "preference"
	MemoryTypeKnowledge  MemoryType = "knowledge"
	MemoryTypeContext    MemoryType = "context"
	MemoryTypeIndex      MemoryType = "index"
)

type Priority int

const (
	PriorityLow    Priority = 0
	PriorityMedium Priority = 5
	PriorityHigh   Priority = 10
)

type MemoryItem struct {
	ID        string                 `json:"id"`
	Type      MemoryType             `json:"type"`
	Key       string                 `json:"key"`
	Value     string                 `json:"value"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Priority  Priority               `json:"priority"`
	Tags      []string               `json:"tags,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	ExpiresAt *time.Time             `json:"expires_at,omitempty"`
}

type IndexNode struct {
	ID            string                 `json:"id"`
	Title         string                 `json:"title"`
	Content       string                 `json:"content,omitempty"`
	Level         int                    `json:"level"`
	StartLine     int                    `json:"start_line,omitempty"`
	EndLine       int                    `json:"end_line,omitempty"`
	Nodes         []*IndexNode           `json:"nodes,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Tags          []string               `json:"tags,omitempty"`
	Summary       string                 `json:"summary,omitempty"`
	PrefixSummary string                 `json:"prefix_summary,omitempty"`
	ParentID      string                 `json:"parent_id,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type IndexTree struct {
	ID        string                `json:"id"`
	RootID    string                `json:"root_id"`
	UserID    string                `json:"user_id"`
	SourceID  string                `json:"source_id"`
	Title     string                `json:"title"`
	Nodes     map[string]*IndexNode `json:"nodes"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

type Preference struct {
	ID        string            `json:"id"`
	UserID    string            `json:"user_id"`
	Category  string            `json:"category"`
	Key       string            `json:"key"`
	Value     string            `json:"value"`
	Priority  Priority          `json:"priority"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type KnowledgeEntry struct {
	ID        string                 `json:"id"`
	UserID    string                 `json:"user_id"`
	Category  string                 `json:"category"`
	Content   string                 `json:"content"`
	Embedding []float32              `json:"embedding,omitempty"`
	Tags      []string               `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Source    string                 `json:"source,omitempty"`
	Priority  Priority               `json:"priority"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type SearchOptions struct {
	Query    string
	Tags     []string
	Limit    int
	Offset   int
	Category string
	Since    *time.Time
	Until    *time.Time
	Priority *Priority
}

type SearchResult struct {
	Items   []MemoryItem `json:"items"`
	Total   int          `json:"total"`
	HasMore bool         `json:"has_more"`
	Query   string       `json:"query,omitempty"`
}

type IndexSearchResult struct {
	Nodes []*IndexNode `json:"nodes"`
	Total int          `json:"total"`
	Query string       `json:"query,omitempty"`
}

type MemoryStats struct {
	PreferenceCount int            `json:"preference_count"`
	KnowledgeCount  int            `json:"knowledge_count"`
	IndexCount      int            `json:"index_count"`
	PageIndexCount  int            `json:"page_index_count"`
	TotalTokens     int            `json:"total_tokens"`
	ByCategory      map[string]int `json:"by_category"`
}

type PageIndexKind string

const (
	PageKindPreference    PageIndexKind = "preference"
	PageKindKnowledge     PageIndexKind = "knowledge"
	PageKindLongTerm      PageIndexKind = "long_term_memory"
	PageKindKnowledgeBase PageIndexKind = "knowledge_base"
)

type PageIndexDoc struct {
	ID        string                 `json:"id"`
	UserID    string                 `json:"user_id"`
	Kind      PageIndexKind          `json:"kind"`
	Title     string                 `json:"title"`
	Text      string                 `json:"text"`
	RefKind   string                 `json:"ref_kind,omitempty"`
	RefID     string                 `json:"ref_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Priority  Priority               `json:"priority"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type PageIndexSearchOptions struct {
	Limit    int
	Kinds    []PageIndexKind
	MinScore float64
}

type PageIndexHit struct {
	Doc   PageIndexDoc `json:"doc"`
	Score float64      `json:"score"`
}

type PageIndexRAGResult struct {
	Hits      []PageIndexHit `json:"hits"`
	Reasoning string         `json:"reasoning,omitempty"`
}

type TreeSearchResult struct {
	Thinking string   `json:"thinking"`
	NodeIDs  []string `json:"node_ids"`
}

type DocSearchCandidate struct {
	DocID          string `json:"doc_id"`
	DocName        string `json:"doc_name"`
	DocDescription string `json:"doc_description"`
}

type MemoryConfig struct {
	MaxPreferences      int
	MaxKnowledge        int
	MaxContext          int
	MaxIndexNodes       int
	ContextTTL          time.Duration
	EnableExpiry        bool
	EnableIndexThinning bool
	MinIndexNodeTokens  int
}

type IndexConfig struct {
	MaxNodesPerTree     int
	MaxContentPerNode   int
	EnableSummary       bool
	SummaryThreshold    int
	VerificationEnabled bool
	MinAccuracy         float32
	MaxRetries          int
	EnableThinning      bool
	MinNodeTokens       int
}

type IndexStore interface {
	CreateIndex(ctx context.Context, userID, sourceID, title string, nodes []*IndexNode) (*IndexTree, error)
	GetIndex(ctx context.Context, userID, sourceID string) (*IndexTree, error)
	UpdateIndex(ctx context.Context, tree *IndexTree) error
	DeleteIndex(ctx context.Context, userID, sourceID string) error
	SearchIndex(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error)
	GetAllIndexes(ctx context.Context, userID string) ([]*IndexTree, error)
	AddNode(ctx context.Context, userID, sourceID string, node *IndexNode, parentID string) error
	RemoveNode(ctx context.Context, userID, sourceID, nodeID string) error
	UpdateNode(ctx context.Context, userID, sourceID string, node *IndexNode) error
}

type PageIndexStore interface {
	Upsert(ctx context.Context, doc PageIndexDoc) error
	Delete(ctx context.Context, userID, id string) error
	DeleteByUser(ctx context.Context, userID string) error
	DeleteByKinds(ctx context.Context, userID string, kinds []PageIndexKind) error
	CountByUser(ctx context.Context, userID string) (int, error)
	Search(ctx context.Context, userID, query string, opts *PageIndexSearchOptions) ([]PageIndexHit, error)
}
