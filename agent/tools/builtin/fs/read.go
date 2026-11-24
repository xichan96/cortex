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

// defaultReadLimit caps an unbounded read window so a huge file cannot flood
// the context. It matches Claude Code's Read default.
const defaultReadLimit = 2000

type ReadTool struct {
	workspace string
}

func NewReadTool(workspace string) types.Tool {
	return &ReadTool{workspace: defaultWorkspace(workspace)}
}

func (t *ReadTool) Name() string {
	return "Read"
}

//go:embed read.txt
var readDescription string

func (t *ReadTool) Description() string {
	return strings.TrimSpace(readDescription + "\n\n" + prompts.ReadToolGuidance)
}

func (t *ReadTool) Schema() map[string]interface{} {
	return schemaObject(map[string]interface{}{
		"file_path": map[string]interface{}{"type": "string", "description": "The absolute path to the file to read"},
		"offset": map[string]interface{}{
			"type":        "integer",
			"description": "1-based line number to start reading from. Only provide when the file is too large to read at once.",
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum number of lines to read. Defaults to 2000 lines from offset.",
		},
	}, []string{"file_path"})
}

func (t *ReadTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := pathArg(input)
	if path == "" {
		return nil, fmt.Errorf("file_path is required")
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
		files, err := os.ReadDir(safePath)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, f := range files {
			if f.IsDir() {
				names = append(names, f.Name()+"/")
			} else {
				names = append(names, f.Name())
			}
		}
		return fmt.Sprintf("Directory listing for %s:\n%s", path, strings.Join(names, "\n")), nil
	}
	content, err := os.ReadFile(safePath)
	if err != nil {
		return nil, err
	}

	offset, hasOffset := intArg(input, "offset")
	limit, hasLimit := intArg(input, "limit")
	if !hasOffset || offset < 1 {
		offset = 1
	}
	if !hasLimit || limit <= 0 {
		limit = defaultReadLimit
	}
	return formatNumberedWindow(path, string(content), offset, limit), nil
}

// formatNumberedWindow renders a 1-based line window as "<line>\t<content>"
// lines (cat -n style), the format the model expects from Read.
func formatNumberedWindow(path, content string, offset, limit int) string {
	lines := splitLines(content)
	total := len(lines)

	start := offset - 1
	if start > total {
		start = total
	}
	if start < 0 {
		start = 0
	}
	end := start + limit
	if end > total {
		end = total
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d\t%s", i+1, lines[i])
	}
	if end < total {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "\n(Showing lines %d-%d of %d. Use offset=%d to continue reading.)", start+1, end, total, end+1)
	}
	return b.String()
}

// splitLines splits file content into logical lines. A trailing newline is a
// terminator, not an extra empty line, so "a\nb\n" is two lines.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
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
	return metadataFSNoCache("Read")
}
