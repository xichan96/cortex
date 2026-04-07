package stores

import (
	"github.com/xichan96/cortex/pkg/memkit/typescore"
	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type (
	Preference             = typescore.Preference
	KnowledgeEntry         = typescore.KnowledgeEntry
	IndexNode              = typescore.IndexNode
	IndexTree              = typescore.IndexTree
	SearchOptions          = typescore.SearchOptions
	SearchResult           = typescore.SearchResult
	IndexSearchResult      = typescore.IndexSearchResult
	MemoryItem             = typescore.MemoryItem
	Priority               = typescore.Priority
	PageIndexDoc           = typescore.PageIndexDoc
	PageIndexKind          = typescore.PageIndexKind
	PageIndexSearchOptions = typescore.PageIndexSearchOptions
	PageIndexHit           = typescore.PageIndexHit
	IndexConfig            = typescore.IndexConfig
	TreeSearchResult       = typescore.TreeSearchResult
	DocSearchCandidate     = typescore.DocSearchCandidate
	IndexStore             = typescore.IndexStore
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

var EstimateTokens = utils.EstimateTokens
var NewID = utils.NewID
var PageKeywordScore = utils.PageKeywordScore
var KindAllowed = utils.KindAllowed

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
