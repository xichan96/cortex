package search

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"

	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GrepTool struct {
	workspace string
}

func NewGrepTool(workspace string) types.Tool {
	return &GrepTool{
		workspace: workspace,
	}
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
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}

	path, _ := input["path"].(string)
	if path == "" {
		if t.workspace == "*" {
			path = "."
		} else {
			path = t.workspace
		}
	}

	absPath, err := fs.SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}

	// Wait, grep might produce relative paths in output if we pass relative path.
	// If we pass absolute path, it produces absolute paths.
	// Dino implementation:
	// path = t.workspace (if empty)
	// isPathInWorkspace(path, t.workspace) -> checks if safe.
	// exec.CommandContext(ctx, "grep", "-r", pattern, path)
	// The path passed to grep is the original path string if it was safe.
	// But isPathInWorkspace in Dino calls fs.SafePath which returns abs path but discards it.
	// So Dino passes `path` (which could be relative or absolute) to `grep`.

	// However, `exec.CommandContext` runs in current working directory?
	// Dino's GrepTool Execute does NOT set cmd.Dir. So it runs in process CWD.
	// If path is relative, it depends on CWD.
	// This seems dangerous/flaky if CWD is not workspace.
	// But let's assume CWD is project root or we should use absolute path.
	// I will use `absPath` returned by `SafePath` to be safe and consistent.

	cmd := exec.CommandContext(ctx, "grep", "-r", pattern, absPath)
	// We don't set Dir, but we use absolute path.

	result, err := cmd.CombinedOutput()
	if err != nil {
		// grep returns exit code 1 if no matches found. This is not necessarily an error for the tool execution.
		// But combined output contains the result or error message.
		// If exit code is 1 and output is empty, it means no matches.
		// If exit code is 2, it means error.
		// But err.Error() usually says "exit status 1".

		// Dino implementation returns error if err != nil.
		// I will do the same to be consistent with Dino.
		// Wait, if grep finds nothing, it returns 1. Dino returns error?
		// Yes: "return string(result), err".
		// So the caller handles it? Or the agent sees it as failure?
		// I'll keep it as is.
		return string(result), errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	return string(result), nil
}

func (t *GrepTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "grep",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
