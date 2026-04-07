package search

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type treeSearchLLMNode struct {
	ID      string              `json:"node_id"`
	Title   string              `json:"title"`
	Level   int                 `json:"level"`
	Summary string              `json:"summary,omitempty"`
	Nodes   []treeSearchLLMNode `json:"nodes,omitempty"`
}

func indexNodeToTreeSearchNode(n *IndexNode) treeSearchLLMNode {
	s := n.Summary
	if s == "" {
		s = n.PrefixSummary
	}
	out := treeSearchLLMNode{ID: n.ID, Title: n.Title, Level: n.Level, Summary: s}
	for _, ch := range n.Nodes {
		out.Nodes = append(out.Nodes, indexNodeToTreeSearchNode(ch))
	}
	return out
}

func indexTreeRootNodes(tree *IndexTree) []*IndexNode {
	if tree == nil || len(tree.Nodes) == 0 {
		return nil
	}
	var roots []*IndexNode
	for _, n := range tree.Nodes {
		if n != nil && n.ParentID == "" {
			roots = append(roots, n)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].StartLine != roots[j].StartLine {
			return roots[i].StartLine < roots[j].StartLine
		}
		return roots[i].ID < roots[j].ID
	})
	return roots
}

func IndexTreeJSONForLLM(tree *IndexTree) ([]byte, error) {
	if tree == nil {
		return nil, fmt.Errorf("memory: tree is nil")
	}
	roots := indexTreeRootNodes(tree)
	if len(roots) == 0 {
		return nil, fmt.Errorf("memory: empty index tree")
	}
	wrap := struct {
		DocTitle string              `json:"doc_title"`
		Nodes    []treeSearchLLMNode `json:"nodes"`
	}{DocTitle: tree.Title}
	for _, r := range roots {
		wrap.Nodes = append(wrap.Nodes, indexNodeToTreeSearchNode(r))
	}
	return json.MarshalIndent(wrap, "", "  ")
}

func FormatPageIndexHitsExpert(hits []PageIndexHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Expert / user context (prefer these sections when relevant):\n")
	for _, h := range hits {
		line := h.Doc.Text
		if len(line) > 200 {
			line = line[:200] + "..."
		}
		fmt.Fprintf(&b, "- [%s] %s: %s\n", h.Doc.Kind, h.Doc.Title, line)
	}
	return b.String()
}

func TreeSearchIndexTree(ctx context.Context, llm LLMProvider, tree *IndexTree, query, expertContext string) (*TreeSearchResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("memory: llm required")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("memory: query required")
	}
	payload, err := IndexTreeJSONForLLM(tree)
	if err != nil {
		return nil, err
	}
	user := fmt.Sprintf(`Query: %s

Document tree structure:
%s`, query, string(payload))
	if strings.TrimSpace(expertContext) != "" {
		user = expertContext + "\n\n" + user
	}
	msg, err := llm.Chat(ctx, []Message{
		{Role: "system", Content: `You are given a query and the tree structure of a document (node_id, title, level, summary, nested nodes).
Find all nodes that are likely to contain the answer. Reply ONLY with JSON:
{"thinking":"<reasoning>","node_list":["node_id1","node_id2"]}
Use exact node_id values from the tree. Return [] if none apply.`},
		{Role: "user", Content: user},
	})
	if err != nil {
		return nil, err
	}
	return parseTreeSearchJSON(msg.Content)
}

func parseTreeSearchJSON(content string) (*TreeSearchResult, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var v struct {
		Thinking string   `json:"thinking"`
		NodeList []string `json:"node_list"`
	}
	if err := json.Unmarshal([]byte(content), &v); err == nil && (len(v.NodeList) > 0 || v.Thinking != "") {
		return &TreeSearchResult{Thinking: v.Thinking, NodeIDs: v.NodeList}, nil
	}
	start := strings.Index(content, `"node_list"`)
	if start == -1 {
		return &TreeSearchResult{Thinking: content}, nil
	}
	sub := content[start:]
	arrStart := strings.Index(sub, "[")
	arrEnd := strings.LastIndex(sub, "]")
	if arrStart < 0 || arrEnd <= arrStart {
		return &TreeSearchResult{Thinking: content}, nil
	}
	var ids []string
	_ = json.Unmarshal([]byte(sub[arrStart:arrEnd+1]), &ids)
	th := ""
	if t := strings.Index(content, `"thinking"`); t >= 0 {
		colon := strings.Index(content[t:], ":")
		if colon > 0 {
			rest := strings.TrimSpace(content[t+colon+1:])
			if strings.HasPrefix(rest, `"`) {
				rest = rest[1:]
				if end := strings.Index(rest, `"`); end > 0 {
					th = rest[:end]
				}
			}
		}
	}
	return &TreeSearchResult{Thinking: th, NodeIDs: ids}, nil
}

func SelectDocIDsByDescriptions(ctx context.Context, llm LLMProvider, query string, docs []DocSearchCandidate) ([]string, string, error) {
	if llm == nil {
		return nil, "", fmt.Errorf("memory: llm required")
	}
	if len(docs) == 0 {
		return nil, "", fmt.Errorf("memory: no documents")
	}
	b, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		return nil, "", err
	}
	msg, err := llm.Chat(ctx, []Message{
		{Role: "system", Content: `You are given documents with id, name, and description. Select doc_ids that may help answer the query.
Reply ONLY JSON: {"thinking":"<reasoning>","answer":["doc_id1"]}. Use [] if none apply.`},
		{Role: "user", Content: "Query: " + query + "\n\nDocuments:\n" + string(b)},
	})
	if err != nil {
		return nil, "", err
	}
	content := strings.TrimSpace(msg.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var v struct {
		Thinking string   `json:"thinking"`
		Answer   []string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(content), &v); err == nil {
		return v.Answer, v.Thinking, nil
	}
	return nil, content, fmt.Errorf("memory: parse doc selection response")
}

func GenerateIndexDocDescription(ctx context.Context, llm LLMProvider, tree *IndexTree) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("memory: llm required")
	}
	payload, err := IndexTreeJSONForLLM(tree)
	if err != nil {
		return "", err
	}
	msg, err := llm.Chat(ctx, []Message{
		{Role: "system", Content: "You are given a table-of-contents style tree of a document. Output one short sentence describing what the document is about, easy to distinguish from other documents. No quotes or preamble."},
		{Role: "user", Content: string(payload)},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(msg.Content), nil
}
