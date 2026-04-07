package sqlite

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
