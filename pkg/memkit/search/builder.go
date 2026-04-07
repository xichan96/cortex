package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/pkg/memkit/stores"
)

type IndexBuilder struct {
	llm    LLMProvider
	parser *MarkdownParser
	config *IndexConfig
	store  IndexStore
}

func NewIndexBuilder(llm LLMProvider, cfg *IndexConfig) *IndexBuilder {
	return NewIndexBuilderWithStore(llm, cfg, nil)
}

func NewIndexBuilderWithStore(llm LLMProvider, cfg *IndexConfig, store IndexStore) *IndexBuilder {
	if cfg == nil {
		cfg = stores.DefaultIndexConfig()
	}
	if store == nil {
		store = stores.NewInMemoryIndexStore()
	}
	return &IndexBuilder{
		llm:    llm,
		parser: stores.NewMarkdownParser(),
		config: cfg,
		store:  store,
	}
}

func (b *IndexBuilder) BuildFromMarkdown(ctx context.Context, userID, sourceID, title, content string) (*IndexTree, error) {
	nodes := b.parser.Parse(content)

	for i, node := range nodes {
		var next *IndexNode
		if i+1 < len(nodes) {
			next = nodes[i+1]
		}
		node.Content = b.parser.ExtractNodeContent(content, node, next)
		if next != nil {
			node.EndLine = next.StartLine - 1
		}
	}

	if b.config.EnableThinning {
		minTok := b.config.MinNodeTokens
		if minTok <= 0 {
			minTok = 5000
		}
		nodes = stores.ThinMarkdownIndexNodes(nodes, minTok)
	}

	roots := indexNodeRoots(nodes)
	for _, r := range roots {
		b.enrichTree(ctx, r)
	}

	finalNodes := flattenIndexPreorder(roots)
	if b.config.VerificationEnabled {
		v := b.verifyAndFixNodes(ctx, finalNodes, content)
		if len(v) != len(finalNodes) {
			stores.RebuildMarkdownHierarchy(v)
		}
		finalNodes = v
	}

	tree, err := b.store.CreateIndex(ctx, userID, sourceID, title, finalNodes)
	if err != nil {
		return nil, err
	}

	return tree, nil
}

func indexNodeRoots(nodes []*IndexNode) []*IndexNode {
	var roots []*IndexNode
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
		}
	}
	return roots
}

func flattenIndexPreorder(roots []*IndexNode) []*IndexNode {
	var out []*IndexNode
	var walk func(*IndexNode)
	walk = func(n *IndexNode) {
		out = append(out, n)
		for _, ch := range n.Nodes {
			walk(ch)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

func (b *IndexBuilder) enrichTree(ctx context.Context, node *IndexNode) {
	for _, ch := range node.Nodes {
		b.enrichTree(ctx, ch)
	}
	if b.llm != nil {
		tags := b.extractTags(ctx, node.Title, node.Content)
		node.Tags = append(node.Tags, tags...)
	}
	if !b.config.EnableSummary {
		return
	}
	text := strings.TrimSpace(node.Content)
	tok := EstimateTokens(text)
	var s string
	if b.llm != nil && tok > b.config.SummaryThreshold {
		s = b.generateSummary(ctx, text)
	} else {
		if len(text) > 200 {
			s = strings.TrimSpace(text[:200])
		} else {
			s = text
		}
	}
	if len(node.Nodes) == 0 {
		node.Summary = s
	} else {
		node.PrefixSummary = s
	}
}

func (b *IndexBuilder) generateSummary(ctx context.Context, content string) string {
	if b.llm == nil {
		return strings.TrimSpace(content[:min(200, len(content))])
	}

	prompt := fmt.Sprintf(`Please provide a concise summary of the following content in 2-3 sentences:

Content: %s

Summary:`, content[:min(len(content), 2000)])

	msg, err := b.llm.Chat(ctx, []Message{
		{Role: "system", Content: "You are a helpful assistant that summarizes content."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return strings.TrimSpace(content[:min(200, len(content))])
	}

	return strings.TrimSpace(msg.Content)
}

func (b *IndexBuilder) extractTags(ctx context.Context, title, content string) []string {
	if b.llm == nil {
		return []string{}
	}

	prompt := fmt.Sprintf(`Extract 3-5 relevant tags from this content. Return as a JSON array of strings.

Title: %s
Content preview: %s

Tags (JSON array):`, title, content[:min(len(content), 500)])

	msg, err := b.llm.Chat(ctx, []Message{
		{Role: "system", Content: "You are a helpful assistant that extracts tags."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return []string{}
	}

	return parseTags(msg.Content)
}

func (b *IndexBuilder) verifyAndFixNodes(ctx context.Context, nodes []*IndexNode, content string) []*IndexNode {
	if b.llm == nil || len(nodes) == 0 {
		return nodes
	}

	var verifiedNodes []*IndexNode
	for _, node := range nodes {
		if b.verifyNode(ctx, node, content) {
			verifiedNodes = append(verifiedNodes, node)
		}
	}

	return verifiedNodes
}

func (b *IndexBuilder) verifyNode(ctx context.Context, node *IndexNode, fullContent string) bool {
	if b.llm == nil {
		return true
	}

	prompt := fmt.Sprintf(`Check if this section title appears in the given content.

Title: %s
Content: %s

Respond with JSON:
{"valid": true/false, "reason": "explanation"}`, node.Title, node.Content[:min(len(node.Content), 1000)])

	msg, err := b.llm.Chat(ctx, []Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return true
	}

	result := parseVerification(msg.Content)
	return result["valid"] == "true"
}

func parseVerification(content string) map[string]string {
	result := make(map[string]string)
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var v struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal([]byte(content), &v); err == nil {
		if v.Valid {
			result["valid"] = "true"
		} else {
			result["valid"] = "false"
		}
		return result
	}
	if strings.Contains(content, `"valid"`) {
		if strings.Contains(content, `"valid": true`) || strings.Contains(content, `"valid":true`) {
			result["valid"] = "true"
		} else {
			result["valid"] = "false"
		}
	}
	return result
}

func parseTags(content string) []string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var arr []string
	if err := json.Unmarshal([]byte(content), &arr); err == nil {
		return arr
	}

	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start == -1 || end == -1 || start >= end {
		return []string{}
	}

	tagsStr := content[start : end+1]
	tagsStr = strings.ReplaceAll(tagsStr, "\"", "")
	tagsStr = strings.ReplaceAll(tagsStr, "'", "")
	tagsStr = strings.TrimSpace(tagsStr)

	if tagsStr == "[]" || tagsStr == "" {
		return []string{}
	}

	tags := strings.Split(tagsStr, ",")
	var result []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			result = append(result, tag)
		}
	}

	return result
}

type ChunkBuilder struct {
	maxTokens     int
	overlapTokens int
	separator     string
}

func NewChunkBuilder(maxTokens, overlapTokens int) *ChunkBuilder {
	return &ChunkBuilder{
		maxTokens:     maxTokens,
		overlapTokens: overlapTokens,
		separator:     "\n\n",
	}
}

func (b *ChunkBuilder) ChunkByTokens(text string, tokenCount func(string) int) []string {
	if tokenCount == nil {
		tokenCount = EstimateTokens
	}

	tokens := tokenCount(text)
	if tokens <= b.maxTokens {
		return []string{text}
	}

	var chunks []string
	lines := strings.Split(text, "\n")
	var currentChunk []string
	currentTokens := 0

	for _, line := range lines {
		lineTokens := tokenCount(line)

		if currentTokens+lineTokens > b.maxTokens && len(currentChunk) > 0 {
			chunk := strings.Join(currentChunk, "\n")
			chunks = append(chunks, chunk)

			overlapLines := b.collectOverlapLines(currentChunk)
			currentChunk = overlapLines
			currentTokens = tokenCount(strings.Join(currentChunk, "\n"))
		}

		currentChunk = append(currentChunk, line)
		currentTokens += lineTokens
	}

	if len(currentChunk) > 0 {
		chunks = append(chunks, strings.Join(currentChunk, "\n"))
	}

	return chunks
}

func (b *ChunkBuilder) collectOverlapLines(chunk []string) []string {
	if b.overlapTokens <= 0 || len(chunk) == 0 {
		return []string{}
	}

	var overlap []string
	accumulated := 0
	for i := len(chunk) - 1; i >= 0; i-- {
		lineTokens := EstimateTokens(chunk[i])
		if accumulated+lineTokens > b.overlapTokens {
			break
		}
		overlap = append([]string{chunk[i]}, overlap...)
		accumulated += lineTokens
	}

	return overlap
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
