package search

import (
	"github.com/xichan96/cortex/pkg/memkit/stores"
	"github.com/xichan96/cortex/pkg/memkit/typescore"
	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type (
	IndexNode              = typescore.IndexNode
	IndexTree              = typescore.IndexTree
	IndexConfig            = typescore.IndexConfig
	IndexSearchResult      = typescore.IndexSearchResult
	TreeSearchResult       = typescore.TreeSearchResult
	DocSearchCandidate     = typescore.DocSearchCandidate
	IndexStore             = typescore.IndexStore
	PageIndexStore         = typescore.PageIndexStore
	PageIndexSearchOptions = typescore.PageIndexSearchOptions
	PageIndexHit           = typescore.PageIndexHit
	PageIndexKind          = typescore.PageIndexKind
	PageIndexRAGResult     = typescore.PageIndexRAGResult
	MemoryItem             = typescore.MemoryItem
	SearchOptions          = typescore.SearchOptions
	SearchResult           = typescore.SearchResult
	LLMProvider            = typescore.LLMProvider
	Message                = typescore.Message
	Tool                   = typescore.Tool
	MarkdownParser         = stores.MarkdownParser
)

const (
	PageKindPreference    = typescore.PageKindPreference
	PageKindKnowledge     = typescore.PageKindKnowledge
	PageKindLongTerm      = typescore.PageKindLongTerm
	PageKindKnowledgeBase = typescore.PageKindKnowledgeBase
)

var EstimateTokens = utils.EstimateTokens
