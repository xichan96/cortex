package runtime

import (
	"sort"
	"strings"
	"unicode"

	"github.com/xichan96/cortex/agent/types"
)

// SearchableTool 声明工具可被 tool_search 按关键词检索。实现方返回搜索信息。
// 不实现此接口的 Deferred 工具回退用 Name+Description 分词
// （ToolMetadata.SearchKeywords 优先于 Description 分词）。
type SearchableTool interface {
	SearchInfo() SearchInfo
}

// SearchInfo 工具的搜索元信息（E2）。
type SearchInfo struct {
	Keywords []string // 额外关键词（除 Name/Description 外的同义词、动作词）
	Category string   // 分组：e.g. "mcp:github", "memory"
	// SearchOnly 预留：true 表示仅可搜索（默认 false，发现后也可调用）。
	SearchOnly bool
}

// token weights: name hits outrank keyword hits outrank description hits.
const (
	weightName  = 3
	weightKw    = 2
	weightDesc  = 1
)

// indexedTool is one searchable document.
type indexedTool struct {
	name     string
	desc     string
	category string

	// Token buckets per field; the score of a query is the weighted sum of hits.
	nameTokens   []string
	keywordTokens []string
	descTokens   []string
}

// docHit records one token occurrence inside a document.
type docHit struct {
	doc int
	pos int // 0=name, 1=keyword, 2=description
}

// ToolSearchIndex 简单的 token → tool 倒排 + 词频评分（轻量替代 BM25）。
// 只索引 Deferred 工具（E2）；对工具名检索（"github"/"memory"/"search"）足够，
// 无 BM25 的 IDF。可选第二阶段换 BM25。
type ToolSearchIndex struct {
	docs   []indexedTool
	tokens map[string][]docHit
}

// SearchResult 返回给模型的命中项。
type SearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
}

// NewToolSearchIndex builds the inverted index from the given tools. Deferred
// tools not implementing SearchableTool fall back to Name + Description
// (+ ToolMetadata.SearchKeywords).
func NewToolSearchIndex(tools []types.Tool) *ToolSearchIndex {
	ix := &ToolSearchIndex{tokens: make(map[string][]docHit)}
	for _, t := range tools {
		doc := indexTool(t)
		if doc == nil {
			continue
		}
		docIdx := len(ix.docs)
		ix.docs = append(ix.docs, *doc)
		addDocHits(ix.tokens, docIdx, doc.nameTokens, 0)
		addDocHits(ix.tokens, docIdx, doc.keywordTokens, 1)
		addDocHits(ix.tokens, docIdx, doc.descTokens, 2)
	}
	return ix
}

// Search returns up to max results, sorted by score descending. No match
// returns an empty slice (never an error).
func (ix *ToolSearchIndex) Search(query string, max int) []SearchResult {
	if ix == nil || len(ix.docs) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	if max <= 0 {
		max = 8
	}
	if max > 50 {
		max = 50
	}

	scores := make(map[int]int, len(ix.docs))
	qTokens := tokenize(query)
	for _, tok := range qTokens {
		for _, hit := range ix.tokens[tok] {
			var w int
			switch hit.pos {
			case 0:
				w = weightName
			case 1:
				w = weightKw
			default:
				w = weightDesc
			}
			scores[hit.doc] += w
		}
	}

	type scored struct {
		doc   int
		score int
	}
	scoredDocs := make([]scored, 0, len(scores))
	for doc, s := range scores {
		scoredDocs = append(scoredDocs, scored{doc: doc, score: s})
	}
	sort.Slice(scoredDocs, func(i, j int) bool {
		if scoredDocs[i].score != scoredDocs[j].score {
			return scoredDocs[i].score > scoredDocs[j].score
		}
		return ix.docs[scoredDocs[i].doc].name < ix.docs[scoredDocs[j].doc].name
	})

	out := make([]SearchResult, 0, min(len(scoredDocs), max))
	for _, s := range scoredDocs {
		if len(out) >= max {
			break
		}
		d := ix.docs[s.doc]
		out = append(out, SearchResult{
			Name:        d.name,
			Description: d.desc,
			Category:    d.category,
		})
	}
	return out
}

// indexTool normalizes one tool into a searchable doc. Returns nil for tools
// with no usable text.
func indexTool(t types.Tool) *indexedTool {
	if t == nil {
		return nil
	}
	name := t.Name()
	desc := t.Description()

	var keywords []string
	var category string
	if si, ok := t.(SearchableTool); ok {
		info := si.SearchInfo()
		keywords = append(keywords, info.Keywords...)
		category = info.Category
	}
	// ToolMetadata.SearchKeywords is the registry-level source; SearchableTool
	// keywords (if any) extend it.
	if kw := t.Metadata().SearchKeywords; len(kw) > 0 {
		keywords = append(keywords, kw...)
	}

	doc := &indexedTool{
		name:           name,
		desc:           desc,
		category:       category,
		nameTokens:     tokenize(name),
		keywordTokens:  tokenize(strings.Join(keywords, " ")),
		descTokens:     tokenize(desc),
	}
	if len(doc.nameTokens) == 0 && len(doc.keywordTokens) == 0 && len(doc.descTokens) == 0 {
		return nil
	}
	return doc
}

func addDocHits(tokens map[string][]docHit, doc int, toks []string, pos int) {
	for _, tok := range toks {
		tokens[tok] = append(tokens[tok], docHit{doc: doc, pos: pos})
	}
}

// tokenize lowercases, splits camelCase and separates on non-alphanumerics.
func tokenize(s string) []string {
	s = camelSplit(s)
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	for _, p := range parts {
		if p = strings.ToLower(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// camelSplit inserts a space before each upper-case letter that follows a
// lower-case letter or digit ("githubTool" -> "github Tool", "gitHub" -> "git Hub").
// Underscore-separated names ("github_list_repos") already split on '_' in
// tokenize and are the common shape for deferred (MCP) tools; camelCase here is
// best-effort subword segmentation.
func camelSplit(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	prevLower := false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			if prevLower {
				b.WriteByte(' ')
			}
			prevLower = false
		} else {
			prevLower = unicode.IsLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
