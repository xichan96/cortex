package memkit

import "github.com/xichan96/cortex/pkg/memkit/typescore"

type (
	MemoryType             = typescore.MemoryType
	Priority               = typescore.Priority
	MemoryItem             = typescore.MemoryItem
	IndexNode              = typescore.IndexNode
	IndexTree              = typescore.IndexTree
	Preference             = typescore.Preference
	KnowledgeEntry         = typescore.KnowledgeEntry
	SearchOptions          = typescore.SearchOptions
	SearchResult           = typescore.SearchResult
	IndexSearchResult      = typescore.IndexSearchResult
	MemoryStats            = typescore.MemoryStats
	PageIndexKind          = typescore.PageIndexKind
	PageIndexDoc           = typescore.PageIndexDoc
	PageIndexSearchOptions = typescore.PageIndexSearchOptions
	PageIndexHit           = typescore.PageIndexHit
	PageIndexRAGResult     = typescore.PageIndexRAGResult
	TreeSearchResult       = typescore.TreeSearchResult
	DocSearchCandidate     = typescore.DocSearchCandidate
	MemoryConfig           = typescore.MemoryConfig
	IndexConfig            = typescore.IndexConfig
)

const (
	MemoryTypePreference  = typescore.MemoryTypePreference
	MemoryTypeKnowledge   = typescore.MemoryTypeKnowledge
	MemoryTypeIndex       = typescore.MemoryTypeIndex
	PriorityLow           = typescore.PriorityLow
	PriorityMedium        = typescore.PriorityMedium
	PriorityHigh          = typescore.PriorityHigh
	PageKindPreference    = typescore.PageKindPreference
	PageKindKnowledge     = typescore.PageKindKnowledge
	PageKindLongTerm      = typescore.PageKindLongTerm
	PageKindKnowledgeBase = typescore.PageKindKnowledgeBase
)

func DefaultIndexConfig() *IndexConfig {
	return &IndexConfig{
		MaxNodesPerTree:     1000,
		MaxContentPerNode:   5000,
		EnableSummary:       true,
		SummaryThreshold:    200,
		VerificationEnabled: true,
		MinAccuracy:         0.8,
		MaxRetries:          3,
		EnableThinning:      false,
		MinNodeTokens:       5000,
	}
}
