package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/tools/builtin/mcp"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/tools/builtin/search"
	"github.com/xichan96/cortex/agent/tools/builtin/task"
	"github.com/xichan96/cortex/agent/tools/builtin/web"
	"github.com/xichan96/cortex/agent/types"
)

// NewReadFileTool returns a tool to read file contents.
// Delegates to agent/tools/builtin/fs.ReadTool
func NewReadFileTool(workspace string) types.Tool {
	return fs.NewReadTool(workspace)
}

// NewWriteFileTool returns a tool to write file contents.
// Delegates to agent/tools/builtin/fs.WriteTool
func NewWriteFileTool(workspace string) types.Tool {
	return fs.NewWriteTool(workspace)
}

// NewEditFileTool returns a tool to edit file contents.
// Delegates to agent/tools/builtin/fs.EditTool
func NewEditFileTool(workspace string) types.Tool {
	return fs.NewEditTool(workspace)
}

// NewBashTool returns a tool to execute shell commands.
// Delegates to agent/tools/builtin/runtime.CommandTool but returns name "bash"
func NewBashTool(workspace string) types.Tool {
	return &bashToolWrapper{tool: runtime.NewCommandTool(workspace)}
}

type bashToolWrapper struct {
	tool types.Tool
}

func (t *bashToolWrapper) Name() string                   { return "bash" }
func (t *bashToolWrapper) Description() string            { return t.tool.Description() }
func (t *bashToolWrapper) Schema() map[string]interface{} { return t.tool.Schema() }
func (t *bashToolWrapper) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return t.tool.Execute(ctx, input)
}
func (t *bashToolWrapper) Metadata() types.ToolMetadata { return t.tool.Metadata() }

// deferredMCPTool wraps an MCP tool so its Exposure reports Deferred without
// mutating the shared *pkg/mcp.MCPTool instance (E1, MCPDeferred config). All
// other fields and execution forward to the wrapped tool.
type deferredMCPTool struct {
	tool types.Tool
}

// NewDeferredMCPTool wraps a tool with ExposureDeferred. Exported so the factory
// can mark MCP tools as deferred (E1).
func NewDeferredMCPTool(tool types.Tool) types.Tool {
	return &deferredMCPTool{tool: tool}
}

func (t *deferredMCPTool) Name() string                    { return t.tool.Name() }
func (t *deferredMCPTool) Description() string             { return t.tool.Description() }
func (t *deferredMCPTool) Schema() map[string]interface{}  { return t.tool.Schema() }
func (t *deferredMCPTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return t.tool.Execute(ctx, input)
}

func (t *deferredMCPTool) Metadata() types.ToolMetadata {
	md := t.tool.Metadata()
	md.Exposure = types.ExposureDeferred
	return md
}

// NewQuestionTool returns a tool to ask the user questions.
// Delegates to agent/tools/builtin/runtime.QuestionTool
func NewQuestionTool() types.Tool {
	return runtime.NewQuestionTool()
}

// NewJobKillTool returns a tool to kill background jobs.
// Delegates to agent/tools/builtin/runtime.JobKillTool
func NewJobKillTool() types.Tool {
	return runtime.NewJobKillTool()
}

// NewJobOutputTool returns a tool to get background job output.
// Delegates to agent/tools/builtin/runtime.JobOutputTool
func NewJobOutputTool() types.Tool {
	return runtime.NewJobOutputTool()
}

// NewGlobTool returns a tool to find files matching a pattern.
// Delegates to agent/tools/builtin/search.GlobTool
func NewGlobTool(workspace string) types.Tool {
	return search.NewGlobTool(workspace)
}

// NewGrepTool returns a tool to search for text in files.
// Delegates to agent/tools/builtin/search.GrepTool
func NewGrepTool(workspace string) types.Tool {
	return search.NewGrepTool(workspace)
}

// NewWebFetchTool returns a tool to fetch web pages.
// Delegates to agent/tools/builtin/web.WebFetchTool
func NewWebFetchTool() types.Tool {
	return web.NewWebFetchTool()
}

// NewWebSearchTool returns a tool to search the web.
// Delegates to agent/tools/builtin/web.WebSearchTool
func NewWebSearchTool() types.Tool {
	return web.NewWebSearchTool()
}

// NewTodoTool returns a tool to manage tasks.
// Delegates to agent/tools/builtin/task.TodoTool
func NewTodoTool() types.Tool {
	return task.NewTodoTool()
}

// NewMCPClientTool returns a tool to interact with MCP servers.
// Delegates to agent/tools/builtin/mcp.MCPClientTool
func NewMCPClientTool() types.Tool {
	return mcp.NewMCPClientTool()
}

type ListDirectoryTool struct {
	workspace string
}

func NewListDirectoryTool(workspace string) *ListDirectoryTool {
	return &ListDirectoryTool{workspace: workspace}
}

func (t *ListDirectoryTool) Name() string {
	return "list_directory"
}

func (t *ListDirectoryTool) Description() string {
	return "List files in a directory"
}

func (t *ListDirectoryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirectoryTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	path, ok := input["path"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid path parameter")
	}

	absPath, err := fs.SafePath(ctx, t.workspace, path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var result []map[string]interface{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, map[string]interface{}{
			"name":     entry.Name(),
			"is_dir":   entry.IsDir(),
			"size":     info.Size(),
			"mode":     info.Mode().String(),
			"mod_time": info.ModTime().Unix(),
		})
	}

	return result, nil
}

func (t *ListDirectoryTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "dino",
		IsFromToolkit:  false,
		ToolType:       "builtin",
		Priority:       10,
	}
}

