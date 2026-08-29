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

type EditTool struct {
	workspace string
}

func NewEditTool(workspace string) types.Tool {
	return &EditTool{workspace: defaultWorkspace(workspace)}
}

func (t *EditTool) Name() string {
	return "edit_file"
}

//go:embed edit_file.txt
var editFileDescription string

func (t *EditTool) Description() string {
	return strings.TrimSpace(editFileDescription + "\n\n" + prompts.EditToolGuidance)
}

func (t *EditTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"path":    map[string]interface{}{"type": "string", "description": "The path to the file to edit"},
		"old_str": map[string]interface{}{"type": "string", "description": "The exact string to search for (single-hunk mode, with new_str)"},
		"new_str": map[string]interface{}{"type": "string", "description": "The string to replace it with (single-hunk mode)"},
		// 多 hunk JSON（tools-codex-eval §4.4 P2）：一次调用改多处，减少 round-trip。
		// 提供 hunks 时 old_str/new_str 忽略。hunk 间顺序敏感——要求互不重叠。
		"hunks": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"old_str": map[string]interface{}{"type": "string", "description": "Exact string to search for in this hunk"},
					"new_str": map[string]interface{}{"type": "string", "description": "Replacement string for this hunk"},
				},
				"required": []string{"old_str", "new_str"},
			},
			"description": "Multiple precise replacements applied in order. Old strings must not overlap. Atomic: if any hunk's old_str is not found, the whole edit fails without applying any hunk.",
		},
	}, []string{"path"})
}

func (t *EditTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("path is required"))
	}

	// 多 hunk 模式：hunks 提供时用数组（old_str/new_str 忽略）；否则单 hunk。
	type hunk struct {
		Old string `json:"old_str"`
		New string `json:"new_str"`
	}
	var hunks []hunk
	if raw, ok := input["hunks"].([]interface{}); ok && len(raw) > 0 {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid hunks: %v", err))
		}
		if err := json.Unmarshal(b, &hunks); err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid hunks: %v", err))
		}
		if len(hunks) == 0 {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("hunks must not be empty"))
		}
		for _, h := range hunks {
			if h.Old == "" {
				return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("hunk old_str is required"))
			}
		}
	} else {
		oldStr, ok := input["old_str"].(string)
		if !ok {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("old_str is required"))
		}
		newStr, ok := input["new_str"].(string)
		if !ok {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("new_str is required"))
		}
		hunks = []hunk{{Old: oldStr, New: newStr}}
	}

	safePath, err := SafePath(ctx, t.workspace, path)
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
	newContent := content
	// 原子性：先在内存里对全部 hunk 做替换，任一找不到即失败，不落盘任何 hunk。
	for i, h := range hunks {
		if !strings.Contains(newContent, h.Old) {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("hunk %d old_str not found in file", i))
		}
		newContent = strings.Replace(newContent, h.Old, h.New, 1)
	}
	if err := os.WriteFile(safePath, []byte(newContent), 0644); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	applied := len(hunks)
	return map[string]interface{}{"path": path, "message": fmt.Sprintf("Successfully edited %s (%d hunk%s)", path, applied, pluralSuffix(applied))}, nil
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (t *EditTool) Metadata() types.ToolMetadata {
	return metadataFS("edit_file")
}
