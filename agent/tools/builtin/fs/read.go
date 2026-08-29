package fs

import (
	"context"
	_ "embed"
	"encoding/json"
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
		"offset": map[string]interface{}{
			"type":        "integer",
			"description": "1-based starting line number. When set, only a line window [offset, offset+limit) is returned instead of the whole file (efficient for large files).",
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Max number of lines to return (with offset). 0 = all lines from offset.",
		},
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
	s := string(content)

	// Line-window read: offset/limit slice the file by line number. Without
	// them the whole content is returned (today's behavior). (tools-codex-eval
	// §8.2 P2 — efficient large-file reads; avoids F1 whole-read truncation.)
	offset, hasOffset := intArg(input, "offset")
	limit, hasLimit := intArg(input, "limit")
	if !hasOffset && !hasLimit {
		return map[string]interface{}{"path": path, "content": s}, nil
	}

	lines := strings.Split(s, "\n")
	totalLines := len(lines)
	// If the file ends with a newline, the trailing "" is an artifact — but
	// keeping it makes line numbering natural. totalLines reports the logical
	// count: lines minus a trailing empty element.
	if totalLines > 0 && lines[totalLines-1] == "" {
		totalLines--
	}

	start := 0
	if hasOffset && offset > 0 {
		start = offset - 1 // 1-based offset -> 0-based index
	}
	if start > totalLines {
		start = totalLines
	}
	end := totalLines
	if hasLimit && limit > 0 {
		end = start + limit
		if end > totalLines {
			end = totalLines
		}
	}
	if start > end {
		start = end
	}

	window := strings.Join(lines[start:end], "\n")
	return map[string]interface{}{
		"path":        path,
		"content":     window,
		"offset":      start + 1, // effective 1-based start line
		"limit":       end - start,
		"total_lines": totalLines,
		"truncated":   end < totalLines,
	}, nil
}

func intArg(input map[string]interface{}, key string) (int, bool) {
	v, ok := input[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func (t *ReadTool) Metadata() types.ToolMetadata {
	return metadataFS("read_file")
}
