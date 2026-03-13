package search

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GlobTool struct{}

func NewGlobTool() types.Tool {
	return &GlobTool{}
}

func (t *GlobTool) Name() string {
	return "glob"
}

//go:embed glob.txt
var globDescription string

func (t *GlobTool) Description() string {
	return globDescription
}

func (t *GlobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The glob pattern to match files with.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	// Placeholder implementation
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}
	return "Glob is not yet implemented.", nil
}

func (t *GlobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "glob",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
