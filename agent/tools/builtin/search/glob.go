package search

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"

	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GlobTool struct {
	workspace string
}

func NewGlobTool(workspace string) types.Tool {
	return &GlobTool{
		workspace: workspace,
	}
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
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}

	searchPath := pattern
	if !filepath.IsAbs(pattern) {
		searchPath = filepath.Join(t.workspace, pattern)
	}

	// Check if the search path is within workspace
	if _, err := fs.SafePath(t.workspace, searchPath); err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}

	matches, err := filepath.Glob(searchPath)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("glob failed: %w", err))
	}

	var filtered []string
	for _, m := range matches {
		if _, err := fs.SafePath(t.workspace, m); err == nil {
			filtered = append(filtered, m)
		}
	}

	return filtered, nil
}

func (t *GlobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "glob",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
