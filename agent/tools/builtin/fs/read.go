package fs

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/xichan96/cortex/agent/tools/prompts"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type ReadTool struct {
	workspace string
}

func NewReadTool(workspace string) types.Tool {
	return &ReadTool{workspace: defaultWorkspace(workspace)}
}

func (t *ReadTool) Name() string {
	return "read_file"
}

//go:embed read_file.txt
var readFileDescription string

func (t *ReadTool) Description() string {
	return strings.TrimSpace(readFileDescription + "\n\n" + prompts.ReadToolGuidance)
}

func (t *ReadTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"path": map[string]interface{}{"type": "string", "description": "The path to the file to read"},
	}, []string{"path"})
}

func (t *ReadTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("path is required"))
	}
	safePath, err := SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}
	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("file not found: %s", path))
		}
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	if info.IsDir() {
		files, err := os.ReadDir(safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
		}
		var names []string
		for _, f := range files {
			if f.IsDir() {
				names = append(names, f.Name()+"/")
			} else {
				names = append(names, f.Name())
			}
		}
		return map[string]interface{}{"path": path, "listing": names}, nil
	}
	content, err := os.ReadFile(safePath)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"path": path, "content": string(content)}, nil
}

func (t *ReadTool) Metadata() types.ToolMetadata {
	return metadataFS("read_file")
}
