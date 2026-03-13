package search

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GrepTool struct{}

func NewGrepTool() types.Tool {
	return &GrepTool{}
}

func (t *GrepTool) Name() string {
	return "grep"
}

//go:embed grep.txt
var grepDescription string

func (t *GrepTool) Description() string {
	return grepDescription
}

func (t *GrepTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The string or regex pattern to search for.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional file or directory path to search in.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	// Placeholder implementation
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}
	return "Grep is not yet implemented.", nil
}

func (t *GrepTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "grep",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
