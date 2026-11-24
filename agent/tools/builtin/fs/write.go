package fs

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/tools/prompts"
	"github.com/xichan96/cortex/agent/types"
)

type WriteTool struct {
	workspace string
}

func NewWriteTool(workspace string) types.Tool {
	return &WriteTool{workspace: defaultWorkspace(workspace)}
}

func (t *WriteTool) Name() string {
	return "Write"
}

//go:embed write.txt
var writeDescription string

func (t *WriteTool) Description() string {
	return strings.TrimSpace(writeDescription + "\n\n" + prompts.WriteToolGuidance + "\n\n" + prompts.DirExistsGuidance)
}

func (t *WriteTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"file_path": map[string]interface{}{"type": "string", "description": "The absolute path to the file to write"},
		"content":   map[string]interface{}{"type": "string", "description": "The content to write to the file"},
	}, []string{"file_path", "content"})
}

func (t *WriteTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := pathArg(input)
	if path == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	content, ok := input["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}
	safePath, err := SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, err
	}
	existed := false
	info, err := os.Stat(safePath)
	switch {
	case err == nil:
		if info.IsDir() {
			return nil, fmt.Errorf("path is a directory, not a file: %s", path)
		}
		existed = true
	case !os.IsNotExist(err):
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return nil, err
	}
	if existed {
		return fmt.Sprintf("The file %s has been updated successfully.", path), nil
	}
	return fmt.Sprintf("File created successfully at: %s", path), nil
}

func (t *WriteTool) Metadata() types.ToolMetadata {
	return metadataFSInvalidating("Write")
}
