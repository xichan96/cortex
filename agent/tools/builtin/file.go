package builtin

import (
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/file"
)

type FileTool struct {
	file file.File
}

func NewFileTool() types.Tool {
	return &FileTool{file: file.New()}
}

func (t *FileTool) Name() string {
	return "file"
}

func (t *FileTool) Description() string {
	return "Perform file and directory operations including read, write, create, delete, copy, move, and list operations."
}

func (t *FileTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "File operation type",
				"enum": []string{
					"read_file",
					"write_file",
					"append_file",
					"create_dir",
					"delete_file",
					"delete_dir",
					"list_dir",
					"exists",
					"copy",
					"move",
					"is_file",
					"is_dir",
				},
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory path",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "File content (required for write_file and append_file)",
			},
			"target_path": map[string]interface{}{
				"type":        "string",
				"description": "Target path (required for copy and move operations)",
			},
		},
		"required": []string{"operation", "path"},
	}
}

func (t *FileTool) Execute(input map[string]interface{}) (interface{}, error) {
	operation, ok := input["operation"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'operation' parameter: must be a string"))
	}

	path, ok := input["path"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'path' parameter: must be a string"))
	}
	if path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'path' parameter cannot be empty"))
	}

	switch operation {
	case "read_file":
		data, err := t.file.ReadFile(path)
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
		err := t.file.WriteFile(path, []byte(content))
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to write file: %w", err))
		}
		return fmt.Sprintf("File written successfully: %s", path), nil

	case "append_file":
		content, ok := input["content"].(string)
		if !ok {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'content' parameter is required for append_file operation"))
		}
		err := t.file.AppendFile(path, []byte(content))
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to append file: %w", err))
		}
		return fmt.Sprintf("Content appended successfully to: %s", path), nil

	case "create_dir":
		err := t.file.Mkdir(path)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to create directory: %w", err))
		}
		return fmt.Sprintf("Directory created successfully: %s", path), nil

	case "delete_file":
		err := t.file.RemoveFile(path)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to delete file: %w", err))
		}
		return fmt.Sprintf("File deleted successfully: %s", path), nil

	case "delete_dir":
		err := t.file.RemoveDir(path)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to delete directory: %w", err))
		}
		return fmt.Sprintf("Directory deleted successfully: %s", path), nil

	case "list_dir":
		entries, err := t.file.ReadDir(path)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to list directory: %w", err))
		}
		return map[string]interface{}{
			"entries": entries,
			"count":   len(entries),
		}, nil

	case "exists":
		exists, err := t.file.Exists(path)
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
		err := t.file.Copy(path, targetPath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to copy file: %w", err))
		}
		return fmt.Sprintf("File copied successfully from %s to %s", path, targetPath), nil

	case "move":
		targetPath, ok := input["target_path"].(string)
		if !ok || targetPath == "" {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'target_path' parameter is required for move operation"))
		}
		err := t.file.Rename(path, targetPath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to move file: %w", err))
		}
		return fmt.Sprintf("File moved successfully from %s to %s", path, targetPath), nil

	case "is_file":
		isFile, err := t.file.IsFile(path)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to check if path is file: %w", err))
		}
		return map[string]interface{}{
			"is_file": isFile,
			"path":    path,
		}, nil

	case "is_dir":
		isDir, err := t.file.IsDir(path)
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
	return types.ToolMetadata{
		SourceNodeName: "file",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
