package stores

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/pkg/memkit/utils"
)

type InMemoryIndexStore struct {
	mu    sync.RWMutex
	trees map[string]map[string]*IndexTree
}

func NewInMemoryIndexStore() *InMemoryIndexStore {
	return &InMemoryIndexStore{
		trees: make(map[string]map[string]*IndexTree),
	}
}

func (s *InMemoryIndexStore) makeUserIndexKey(userID, sourceID string) string {
	return fmt.Sprintf("%s:%s", userID, sourceID)
}

func (s *InMemoryIndexStore) CreateIndex(ctx context.Context, userID, sourceID, title string, nodes []*IndexNode) (*IndexTree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if sourceID == "" {
		return nil, fmt.Errorf("source_id is required")
	}

	now := time.Now()
	rootID := uuid.New().String()

	nodeMap := make(map[string]*IndexNode)
	for _, node := range nodes {
		if node.ID == "" {
			node.ID = uuid.New().String()
		}
		node.CreatedAt = now
		node.UpdatedAt = now
		nodeMap[node.ID] = node
	}
	for _, node := range nodes {
		if node.ParentID == "" {
			rootID = node.ID
			break
		}
	}

	tree := &IndexTree{
		RootID:    rootID,
		UserID:    userID,
		SourceID:  sourceID,
		Title:     title,
		Nodes:     nodeMap,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if s.trees[userID] == nil {
		s.trees[userID] = make(map[string]*IndexTree)
	}
	s.trees[userID][sourceID] = tree

	return tree, nil
}

func (s *InMemoryIndexStore) GetIndex(ctx context.Context, userID, sourceID string) (*IndexTree, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.trees[userID] == nil {
		return nil, nil
	}

	tree, ok := s.trees[userID][sourceID]
	if !ok {
		return nil, nil
	}

	return cloneIndexTreeByParentID(tree), nil
}

func (s *InMemoryIndexStore) UpdateIndex(ctx context.Context, tree *IndexTree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.trees[tree.UserID] == nil {
		return fmt.Errorf("index not found")
	}
	if s.trees[tree.UserID][tree.SourceID] == nil {
		return fmt.Errorf("index not found")
	}

	tree.UpdatedAt = time.Now()
	s.trees[tree.UserID][tree.SourceID] = tree

	return nil
}

func (s *InMemoryIndexStore) DeleteIndex(ctx context.Context, userID, sourceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.trees[userID] != nil {
		delete(s.trees[userID], sourceID)
	}

	return nil
}

func (s *InMemoryIndexStore) SearchIndex(ctx context.Context, userID, query string, limit int) (*IndexSearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	var matchedNodes []*IndexNode
	queryLower := strings.ToLower(query)

	userTrees := s.trees[userID]
	if userTrees == nil {
		return &IndexSearchResult{Nodes: matchedNodes, Total: 0, Query: query}, nil
	}

	for _, tree := range userTrees {
		for _, node := range tree.Nodes {
			if s.matchNode(node, queryLower) {
				nodeCopy := *node
				nodeCopy.Content = ""
				nodeCopy.Nodes = nil
				matchedNodes = append(matchedNodes, &nodeCopy)
			}
		}
	}

	sort.Slice(matchedNodes, func(i, j int) bool {
		if matchedNodes[i].Level != matchedNodes[j].Level {
			return matchedNodes[i].Level < matchedNodes[j].Level
		}
		return strings.Contains(strings.ToLower(matchedNodes[i].Title), queryLower)
	})

	total := len(matchedNodes)
	if len(matchedNodes) > limit {
		matchedNodes = matchedNodes[:limit]
	}

	return &IndexSearchResult{
		Nodes: matchedNodes,
		Total: total,
		Query: query,
	}, nil
}

func (s *InMemoryIndexStore) GetAllIndexes(ctx context.Context, userID string) ([]*IndexTree, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*IndexTree
	if s.trees[userID] != nil {
		for _, tree := range s.trees[userID] {
			result = append(result, tree)
		}
	}

	return result, nil
}

func (s *InMemoryIndexStore) AddNode(ctx context.Context, userID, sourceID string, node *IndexNode, parentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.trees[userID] == nil || s.trees[userID][sourceID] == nil {
		return fmt.Errorf("index not found")
	}

	tree := s.trees[userID][sourceID]
	now := time.Now()

	if node.ID == "" {
		node.ID = uuid.New().String()
	}
	node.CreatedAt = now
	node.UpdatedAt = now
	node.ParentID = parentID

	tree.Nodes[node.ID] = node

	if parentID != "" && tree.Nodes[parentID] != nil {
		tree.Nodes[parentID].Nodes = append(tree.Nodes[parentID].Nodes, node)
	}

	tree.UpdatedAt = now

	return nil
}

func (s *InMemoryIndexStore) RemoveNode(ctx context.Context, userID, sourceID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.trees[userID] == nil || s.trees[userID][sourceID] == nil {
		return fmt.Errorf("index not found")
	}

	tree := s.trees[userID][sourceID]
	node := tree.Nodes[nodeID]
	if node == nil {
		return fmt.Errorf("node not found")
	}

	if node.ParentID != "" && tree.Nodes[node.ParentID] != nil {
		parent := tree.Nodes[node.ParentID]
		var newChildren []*IndexNode
		for _, child := range parent.Nodes {
			if child.ID != nodeID {
				newChildren = append(newChildren, child)
			}
		}
		parent.Nodes = newChildren
	}

	s.removeNodeRecursive(tree, nodeID)

	return nil
}

func (s *InMemoryIndexStore) removeNodeRecursive(tree *IndexTree, nodeID string) {
	node := tree.Nodes[nodeID]
	if node != nil {
		for _, child := range node.Nodes {
			s.removeNodeRecursive(tree, child.ID)
		}
	}
	delete(tree.Nodes, nodeID)
}

func (s *InMemoryIndexStore) UpdateNode(ctx context.Context, userID, sourceID string, node *IndexNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.trees[userID] == nil || s.trees[userID][sourceID] == nil {
		return fmt.Errorf("index not found")
	}

	tree := s.trees[userID][sourceID]
	if tree.Nodes[node.ID] == nil {
		return fmt.Errorf("node not found")
	}

	node.UpdatedAt = time.Now()
	tree.Nodes[node.ID] = node
	tree.UpdatedAt = time.Now()

	return nil
}

func (s *InMemoryIndexStore) matchNode(node *IndexNode, query string) bool {
	if strings.Contains(strings.ToLower(node.Title), query) {
		return true
	}
	if strings.Contains(strings.ToLower(node.Content), query) {
		return true
	}
	if strings.Contains(strings.ToLower(node.Summary), query) {
		return true
	}
	if strings.Contains(strings.ToLower(node.PrefixSummary), query) {
		return true
	}
	for _, tag := range node.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

type MarkdownParser struct {
	headerPattern *regexp.Regexp
	codeFence     *regexp.Regexp
}

func deepCopyIndexNodeFlat(n *IndexNode) *IndexNode {
	if n == nil {
		return nil
	}
	cp := &IndexNode{}
	*cp = *n
	if n.Metadata != nil {
		cp.Metadata = make(map[string]interface{})
		for k, v := range n.Metadata {
			cp.Metadata[k] = v
		}
	}
	if n.Tags != nil {
		cp.Tags = append([]string(nil), n.Tags...)
	}
	cp.Nodes = nil
	return cp
}

func cloneIndexTreeByParentID(tree *IndexTree) *IndexTree {
	out := *tree
	out.Nodes = make(map[string]*IndexNode, len(tree.Nodes))
	for _, v := range tree.Nodes {
		out.Nodes[v.ID] = deepCopyIndexNodeFlat(v)
	}
	for _, v := range out.Nodes {
		if v.ParentID != "" {
			if p, ok := out.Nodes[v.ParentID]; ok {
				p.Nodes = append(p.Nodes, v)
			}
		}
	}
	for _, v := range out.Nodes {
		if v.Nodes == nil {
			v.Nodes = []*IndexNode{}
		}
	}
	return &out
}

func NewMarkdownParser() *MarkdownParser {
	return &MarkdownParser{
		headerPattern: regexp.MustCompile(`^(#{1,6})\s+(.+)$`),
		codeFence:     regexp.MustCompile("^```"),
	}
}

func (p *MarkdownParser) Parse(content string) []*IndexNode {
	var nodes []*IndexNode
	lines := strings.Split(content, "\n")
	var stack []*IndexNode
	inCodeBlock := false

	for lineNum, line := range lines {
		stripped := strings.TrimSpace(line)
		if p.codeFence.MatchString(stripped) {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		if stripped == "" {
			continue
		}

		match := p.headerPattern.FindStringSubmatch(stripped)
		if match == nil {
			continue
		}

		level := len(match[1])
		title := strings.TrimSpace(match[2])
		now := time.Now()

		node := &IndexNode{
			ID:        uuid.New().String(),
			Title:     title,
			Level:     level,
			StartLine: lineNum + 1,
			Nodes:     []*IndexNode{},
			Tags:      []string{},
			Metadata:  make(map[string]interface{}),
			CreatedAt: now,
			UpdatedAt: now,
		}

		for len(stack) > 0 && stack[len(stack)-1].Level >= level {
			popped := stack[len(stack)-1]
			popped.EndLine = lineNum
			stack = stack[:len(stack)-1]
		}

		if len(stack) > 0 {
			node.ParentID = stack[len(stack)-1].ID
			stack[len(stack)-1].Nodes = append(stack[len(stack)-1].Nodes, node)
		}

		nodes = append(nodes, node)
		stack = append(stack, node)
	}

	for _, node := range stack {
		node.EndLine = len(lines)
	}

	return nodes
}

func (p *MarkdownParser) ExtractNodeContent(content string, node *IndexNode, nextNode *IndexNode) string {
	lines := strings.Split(content, "\n")

	start := node.StartLine - 1
	end := len(lines)
	if nextNode != nil {
		end = nextNode.StartLine - 1
	} else if node.EndLine > 0 {
		end = node.EndLine
	}

	if start >= len(lines) {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}

	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func markdownDescendantIndices(nodes []*IndexNode, i int) []int {
	var idx []int
	level := nodes[i].Level
	for j := i + 1; j < len(nodes); j++ {
		if nodes[j].Level <= level {
			break
		}
		idx = append(idx, j)
	}
	return idx
}

func computeMarkdownSubtreeTokenEstimates(nodes []*IndexNode) []int {
	n := len(nodes)
	counts := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		var b strings.Builder
		b.WriteString(nodes[i].Content)
		for _, j := range markdownDescendantIndices(nodes, i) {
			if nodes[j].Content != "" {
				b.WriteString("\n")
				b.WriteString(nodes[j].Content)
			}
		}
		counts[i] = utils.EstimateTokens(b.String())
	}
	return counts
}

func RebuildMarkdownHierarchy(nodes []*IndexNode) {
	for _, n := range nodes {
		n.Nodes = nil
		n.ParentID = ""
	}
	var stack []*IndexNode
	for _, node := range nodes {
		for len(stack) > 0 && stack[len(stack)-1].Level >= node.Level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			node.ParentID = stack[len(stack)-1].ID
			stack[len(stack)-1].Nodes = append(stack[len(stack)-1].Nodes, node)
		}
		stack = append(stack, node)
	}
}

func ThinMarkdownIndexNodes(nodes []*IndexNode, minTokens int) []*IndexNode {
	if minTokens <= 0 || len(nodes) == 0 {
		return nodes
	}
	n := len(nodes)
	counts := computeMarkdownSubtreeTokenEstimates(nodes)
	remove := make([]bool, n)

	for i := n - 1; i >= 0; i-- {
		if remove[i] {
			continue
		}
		if counts[i] >= minTokens {
			continue
		}
		desc := markdownDescendantIndices(nodes, i)
		merged := nodes[i].Content
		for _, j := range desc {
			if remove[j] {
				continue
			}
			ct := nodes[j].Content
			remove[j] = true
			if ct == "" {
				continue
			}
			if merged != "" && !strings.HasSuffix(merged, "\n") {
				merged += "\n\n"
			}
			merged += ct
		}
		nodes[i].Content = strings.TrimSpace(merged)
		counts[i] = utils.EstimateTokens(nodes[i].Content)
	}

	out := make([]*IndexNode, 0, n)
	for i := range nodes {
		if !remove[i] {
			out = append(out, nodes[i])
		}
	}
	RebuildMarkdownHierarchy(out)
	return out
}
