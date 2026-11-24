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
)

type EditTool struct {
	workspace string
}

func NewEditTool(workspace string) types.Tool {
	return &EditTool{workspace: defaultWorkspace(workspace)}
}

func (t *EditTool) Name() string {
	return "Edit"
}

//go:embed edit.txt
var editDescription string

func (t *EditTool) Description() string {
	return strings.TrimSpace(editDescription + "\n\n" + prompts.EditToolGuidance)
}

func (t *EditTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"file_path":   map[string]interface{}{"type": "string", "description": "The absolute path to the file to modify"},
		"old_string":  map[string]interface{}{"type": "string", "description": "The exact text to replace. Must be unique in the file unless replace_all is true."},
		"new_string":  map[string]interface{}{"type": "string", "description": "The text to replace it with"},
		"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace every occurrence of old_string (default false)"},
		// 多 hunk（cortex 扩展，可选）：一次调用改多处，减少 round-trip。
		// 提供 hunks 时 file_path 之外的四参数被忽略。hunk 间顺序敏感——要求互不重叠。
		"hunks": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"old_string": map[string]interface{}{"type": "string", "description": "Exact string to search for in this hunk"},
					"new_string": map[string]interface{}{"type": "string", "description": "Replacement string for this hunk"},
				},
				"required": []string{"old_string", "new_string"},
			},
			"description": "Optional extension: multiple precise replacements applied in order. Old strings must not overlap. Atomic: if any hunk's old_string is not found, the whole edit fails without applying any hunk.",
		},
		// old_string/new_string are required unless the optional hunks extension
		// is used, so requiredness is enforced in Execute rather than here.
	}, []string{"file_path"})
}

func (t *EditTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := pathArg(input)
	if path == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	replaceAll, _ := input["replace_all"].(bool)

	// 多 hunk 模式：hunks 提供时用数组；否则单替换（Claude 四参数）。
	type rawHunk struct {
		Old       string `json:"old_string"`
		New       string `json:"new_string"`
		OldLegacy string `json:"old_str"`
		NewLegacy string `json:"new_str"`
	}
	type hunk struct {
		Old string
		New string
	}
	var hunks []hunk
	if raw, ok := input["hunks"].([]interface{}); ok && len(raw) > 0 {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid hunks: %v", err)
		}
		var rawHunks []rawHunk
		if err := json.Unmarshal(b, &rawHunks); err != nil {
			return nil, fmt.Errorf("invalid hunks: %v", err)
		}
		for _, h := range rawHunks {
			if h.Old == "" {
				h.Old = h.OldLegacy
			}
			if h.New == "" {
				h.New = h.NewLegacy
			}
			if h.Old == "" {
				return nil, fmt.Errorf("hunk old_string is required")
			}
			hunks = append(hunks, hunk{Old: h.Old, New: h.New})
		}
	} else {
		oldStr, ok := input["old_string"].(string)
		if !ok {
			// Backward-compatible key for in-process callers.
			oldStr, ok = input["old_str"].(string)
		}
		if !ok {
			return nil, fmt.Errorf("old_string is required")
		}
		newStr, ok := input["new_string"].(string)
		if !ok {
			newStr, ok = input["new_str"].(string)
		}
		if !ok {
			return nil, fmt.Errorf("new_string is required")
		}
		hunks = []hunk{{Old: oldStr, New: newStr}}
	}

	safePath, err := SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("File does not exist: %s", path)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot edit directory: %s", path)
	}
	contentBytes, err := os.ReadFile(safePath)
	if err != nil {
		return nil, err
	}
	content := string(contentBytes)
	newContent := content
	// 原子性：先在内存里对全部 hunk 做替换，任一失败即整体失败，不落盘任何 hunk。
	for _, h := range hunks {
		count := strings.Count(newContent, h.Old)
		if count == 0 {
			return nil, fmt.Errorf("old_string not found in %s: found 0 matches", path)
		}
		if replaceAll {
			newContent = strings.ReplaceAll(newContent, h.Old, h.New)
			continue
		}
		if count > 1 {
			return nil, fmt.Errorf("old_string is not unique in %s: found %d matches. Provide more surrounding context or set replace_all=true.", path, count)
		}
		newContent = strings.Replace(newContent, h.Old, h.New, 1)
	}
	if err := os.WriteFile(safePath, []byte(newContent), 0644); err != nil {
		return nil, err
	}
	return fmt.Sprintf("The file %s has been updated successfully.", path), nil
}

func (t *EditTool) Metadata() types.ToolMetadata {
	return metadataFSInvalidating("Edit")
}
