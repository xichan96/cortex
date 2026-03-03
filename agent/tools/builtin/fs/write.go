package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type WriteTool struct {
	workspace string
}

func NewWriteTool(workspace string) types.Tool {
	return &WriteTool{workspace: defaultWorkspace(workspace)}
}

func (t *WriteTool) Name() string {
	return "write_file"
}

func (t *WriteTool) Description() string {
	return "Writes content to a file in the workspace. Overwrites existing files."
}

func (t *WriteTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"path":    map[string]interface{}{"type": "string", "description": "The path to the file to write"},
		"content": map[string]interface{}{"type": "string", "description": "The content to write"},
	}, []string{"path", "content"})
}

func (t *WriteTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("path is required"))
	}
	content, ok := input["content"].(string)
	if !ok {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("content is required"))
	}
	safePath, err := SafePath(t.workspace, path)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}
	info, err := os.Stat(safePath)
	if err == nil {
		if info.IsDir() {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("path is a directory, not a file: %s", path))
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"path": path, "message": "Successfully wrote to " + path}, nil
}

func (t *WriteTool) Metadata() types.ToolMetadata {
	return metadataFS("write_file")
}
