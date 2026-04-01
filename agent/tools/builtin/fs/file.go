package fs

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/file"
)

type FileTool struct {
	workspace string
	file      file.File
}

func NewFileTool(workspace string) types.Tool {
	return &FileTool{workspace: defaultWorkspace(workspace), file: file.New()}
}

func (t *FileTool) Name() string {
	return "file"
}

//go:embed file.txt
var fileDescription string

func (t *FileTool) Description() string {
	return fileDescription
}

func (t *FileTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"operation": map[string]interface{}{
			"type":        "string",
			"description": "File operation type",
			"enum": []string{
				"read_file", "write_file", "append_file", "create_dir",
				"delete_file", "delete_dir", "list_dir", "exists",
				"copy", "move", "is_file", "is_dir",
			},
		},
		"path":        map[string]interface{}{"type": "string", "description": "File or directory path"},
		"content":     map[string]interface{}{"type": "string", "description": "File content (required for write_file and append_file)"},
		"target_path": map[string]interface{}{"type": "string", "description": "Target path (required for copy and move operations)"},
	}, []string{"operation", "path"})
}

func (t *FileTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, ok := input["operation"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'operation' parameter: must be a string"))
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'path' parameter is required"))
	}
	safePath, err := SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}
	switch operation {
	case "read_file":
		data, err := t.file.ReadFile(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to read file: %w", err))
		}
		return map[string]interface{}{
			"content": string(data),
			"size":    len(data),
		}, nil

	case "write_file":
		content, ok := input["content"].(string)
		if !ok {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'content' parameter is required for write_file operation"))
		}
		err := t.file.WriteFile(ctx, safePath, []byte(content))
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to write file: %w", err))
		}
		return fmt.Sprintf("File written successfully: %s", path), nil

	case "append_file":
		content, ok := input["content"].(string)
		if !ok {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'content' parameter is required for append_file operation"))
		}
		err := t.file.AppendFile(ctx, safePath, []byte(content))
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to append file: %w", err))
		}
		return fmt.Sprintf("Content appended successfully to: %s", path), nil

	case "create_dir":
		err := t.file.Mkdir(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to create directory: %w", err))
		}
		return fmt.Sprintf("Directory created successfully: %s", path), nil

	case "delete_file":
		err := t.file.RemoveFile(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to delete file: %w", err))
		}
		return fmt.Sprintf("File deleted successfully: %s", path), nil

	case "delete_dir":
		err := t.file.RemoveDir(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to delete directory: %w", err))
		}
		return fmt.Sprintf("Directory deleted successfully: %s", path), nil

	case "list_dir":
		entries, err := t.file.ReadDir(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to list directory: %w", err))
		}
		return map[string]interface{}{
			"entries": entries,
			"count":   len(entries),
		}, nil

	case "exists":
		exists, err := t.file.Exists(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to check existence: %w", err))
		}
		return map[string]interface{}{
			"exists": exists,
			"path":   path,
		}, nil

	case "copy":
		targetPath, ok := input["target_path"].(string)
		if !ok || targetPath == "" {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'target_path' parameter is required for copy operation"))
		}
		safeTarget, err := SafePath(ctx, t.workspace, targetPath)
		if err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
		}
		err = t.file.Copy(ctx, safePath, safeTarget)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to copy file: %w", err))
		}
		return fmt.Sprintf("File copied successfully from %s to %s", path, targetPath), nil

	case "move":
		targetPath, ok := input["target_path"].(string)
		if !ok || targetPath == "" {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'target_path' parameter is required for move operation"))
		}
		safeTarget, err := SafePath(ctx, t.workspace, targetPath)
		if err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
		}
		err = t.file.Rename(ctx, safePath, safeTarget)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to move file: %w", err))
		}
		return fmt.Sprintf("File moved successfully from %s to %s", path, targetPath), nil

	case "is_file":
		isFile, err := t.file.IsFile(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to check if path is file: %w", err))
		}
		return map[string]interface{}{
			"is_file": isFile,
			"path":    path,
		}, nil

	case "is_dir":
		isDir, err := t.file.IsDir(ctx, safePath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to check if path is directory: %w", err))
		}
		return map[string]interface{}{
			"is_dir": isDir,
			"path":   path,
		}, nil

	default:
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("unsupported operation: %s", operation))
	}
}

func (t *FileTool) Metadata() types.ToolMetadata {
	return metadataFS("file")
}
