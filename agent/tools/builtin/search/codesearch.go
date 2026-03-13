package search

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type CodeSearchTool struct{}

func NewCodeSearchTool() types.Tool {
	return &CodeSearchTool{}
}

func (t *CodeSearchTool) Name() string {
	return "codesearch"
}

//go:embed codesearch.txt
var codeSearchDescription string

func (t *CodeSearchTool) Description() string {
	return codeSearchDescription
}

func (t *CodeSearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Natural language search query",
			},
		},
		"required": []string{"query"},
	}
}

func (t *CodeSearchTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	// Placeholder implementation
	query, ok := input["query"].(string)
	if !ok || query == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("query is required"))
	}
	return "Code search is not yet implemented.", nil
}

func (t *CodeSearchTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "codesearch",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
