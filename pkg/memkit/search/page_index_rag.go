package search

import (
	"context"
	"fmt"
	"strings"
)

type PageIndexRAG struct {
	Store PageIndexStore
	LLM   LLMProvider
	Limit int
}

func NewPageIndexRAG(store PageIndexStore, llm LLMProvider) *PageIndexRAG {
	return &PageIndexRAG{Store: store, LLM: llm, Limit: 12}
}

func (r *PageIndexRAG) Retrieve(ctx context.Context, userID, query string, kinds []PageIndexKind) (*PageIndexRAGResult, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("memory: page index store is nil")
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 12
	}
	hits, err := r.Store.Search(ctx, userID, query, &PageIndexSearchOptions{Limit: limit, Kinds: kinds})
	if err != nil {
		return nil, err
	}
	out := &PageIndexRAGResult{Hits: hits}
	if r.LLM == nil || len(hits) == 0 || strings.TrimSpace(query) == "" {
		return out, nil
	}
	out.Reasoning = r.reason(ctx, query, hits)
	return out, nil
}

func (r *PageIndexRAG) reason(ctx context.Context, query string, hits []PageIndexHit) string {
	var b strings.Builder
	for i, h := range hits {
		if i >= 8 {
			break
		}
		snippet := h.Doc.Text
		if len(snippet) > 400 {
			snippet = snippet[:400] + "..."
		}
		fmt.Fprintf(&b, "[%d] kind=%s title=%s\n%s\n\n", i+1, h.Doc.Kind, h.Doc.Title, snippet)
	}
	msg, err := r.LLM.Chat(ctx, []Message{
		{Role: "system", Content: "You relate user query to retrieved memory snippets. Reply in 2-4 short sentences: which items matter and why. If none fit, say so."},
		{Role: "user", Content: "Query: " + query + "\n\nSnippets:\n" + b.String()},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Content)
}
