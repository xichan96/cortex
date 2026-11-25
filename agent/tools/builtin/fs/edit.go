package fs

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	unlock := lockPathMutation(safePath)
	defer unlock()
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
	raw, err := os.ReadFile(safePath)
	if err != nil {
		return nil, err
	}
	bom := bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	content := string(raw)
	if bom {
		content = content[3:]
	}
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	// 原子性：先在内存里对全部 hunk 做替换，任一失败即整体失败，不落盘任何 hunk。
	newContent := content
	for _, h := range hunks {
		newContent, err = applyEdit(newContent, h.Old, h.New, replaceAll, path)
		if err != nil {
			return nil, err
		}
	}
	out := newContent
	if eol == "\r\n" {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if bom {
		out = "\ufeff" + out
	}
	if err := atomicWriteFile(safePath, []byte(out), info.Mode().Perm()); err != nil {
		return nil, err
	}
	return fmt.Sprintf("The file %s has been updated successfully.", path), nil
}

// applyEdit performs one replacement. Exact substring matching is tried first
// (it handles partial-line anchors the way Claude's Edit does); if that finds
// nothing, a per-line tolerance ladder handles whitespace/typographic
// differences the model commonly introduces. Uniqueness is enforced at the
// level that matches, so ambiguous edits fail with line numbers.
func applyEdit(content, old, new string, replaceAll bool, path string) (string, error) {
	if old == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	if count := strings.Count(content, old); count > 0 {
		if replaceAll {
			return strings.ReplaceAll(content, old, new), nil
		}
		if count > 1 {
			return "", fmt.Errorf("old_string is not unique in %s: found %d matches at line(s) %s. Provide more surrounding context or set replace_all=true.",
				path, count, exactMatchLines(content, old))
		}
		return strings.Replace(content, old, new, 1), nil
	}

	lines := splitLines(content)
	oldLines := splitLines(old)
	if len(oldLines) == 0 {
		return "", fmt.Errorf("old_string must not be empty")
	}
	for level := 1; level <= 3; level++ {
		starts := findLineRanges(lines, oldLines, level)
		if len(starts) == 0 {
			continue
		}
		if len(starts) > 1 && !replaceAll {
			return "", fmt.Errorf("old_string is not unique in %s: found %d matches at line(s) %s. Provide more surrounding context or set replace_all=true.",
				path, len(starts), formatLineNumbers(starts))
		}
		newLines := splitLines(new)
		if replaceAll {
			for i := len(starts) - 1; i >= 0; i-- {
				lines = spliceLines(lines, starts[i], len(oldLines), newLines)
			}
			return strings.Join(lines, "\n"), nil
		}
		lines = spliceLines(lines, starts[0], len(oldLines), newLines)
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("old_string not found in %s: found 0 matches", path)
}

func lineEqual(a, b string, level int) bool {
	switch level {
	case 0:
		return a == b
	case 1:
		return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t")
	case 2:
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	default:
		return strings.TrimSpace(normalizeTypography(a)) == strings.TrimSpace(normalizeTypography(b))
	}
}

func findLineRanges(lines, oldLines []string, level int) []int {
	var starts []int
	for i := 0; i+len(oldLines) <= len(lines); i++ {
		ok := true
		for j := range oldLines {
			if !lineEqual(lines[i+j], oldLines[j], level) {
				ok = false
				break
			}
		}
		if ok {
			starts = append(starts, i)
		}
	}
	return starts
}

func spliceLines(lines []string, start, remove int, add []string) []string {
	out := make([]string, 0, len(lines)-remove+len(add))
	out = append(out, lines[:start]...)
	out = append(out, add...)
	out = append(out, lines[start+remove:]...)
	return out
}

func exactMatchLines(content, old string) string {
	var lineNos []int
	line := 1
	for i := 0; ; {
		idx := strings.Index(content[i:], old)
		if idx < 0 {
			break
		}
		pos := i + idx
		lineNos = append(lineNos, line+strings.Count(content[:pos], "\n"))
		i = pos + len(old)
	}
	return joinInts(lineNos)
}

func formatLineNumbers(starts []int) string {
	nos := make([]int, len(starts))
	for i, s := range starts {
		nos[i] = s + 1
	}
	return joinInts(nos)
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func normalizeTypography(s string) string {
	return strings.NewReplacer(
		"‘", "'", "’", "'", "“", "\"", "”", "\"",
		"–", "-", "—", "-", " ", " ",
	).Replace(s)
}

// atomicWriteFile writes through a temp file in the same directory and renames
// it into place, so a crash or partial write cannot truncate the target.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cortex-edit-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (t *EditTool) Metadata() types.ToolMetadata {
	return metadataFSInvalidating("Edit")
}
