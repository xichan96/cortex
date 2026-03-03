package fs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type EditTool struct {
	workspace string
}

func NewEditTool(workspace string) types.Tool {
	return &EditTool{workspace: defaultWorkspace(workspace)}
}

func (t *EditTool) Name() string {
	return "edit_file"
}

func (t *EditTool) Description() string {
	return "Edits a file by replacing a text segment with new content (first occurrence)."
}

func (t *EditTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"path":    map[string]interface{}{"type": "string", "description": "The path to the file to edit"},
		"old_str": map[string]interface{}{"type": "string", "description": "The exact string to search for"},
		"new_str": map[string]interface{}{"type": "string", "description": "The string to replace it with"},
	}, []string{"path", "old_str", "new_str"})
}

func (t *EditTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("path is required"))
	}
	oldStr, ok := input["old_str"].(string)
	if !ok {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("old_str is required"))
	}
	newStr, ok := input["new_str"].(string)
	if !ok {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("new_str is required"))
	}
	safePath, err := SafePath(t.workspace, path)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}
	info, err := os.Stat(safePath)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	if info.IsDir() {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("cannot edit directory: %s", path))
	}
	contentBytes, err := os.ReadFile(safePath)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	content := string(contentBytes)
	if !strings.Contains(content, oldStr) {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("old_str not found in file"))
	}
	newContent := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(safePath, []byte(newContent), 0644); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"path": path, "message": "Successfully edited " + path}, nil
}

func (t *EditTool) Metadata() types.ToolMetadata {
	return metadataFS("edit_file")
}
