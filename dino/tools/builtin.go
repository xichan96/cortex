package tools

import (
	"context"

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
// Delegates to agent/tools/builtin/runtime.CommandTool but exposes the
// Claude Code-compatible name "Bash" and schema.
func NewBashTool(workspace string) types.Tool {
	return &bashToolWrapper{tool: runtime.NewCommandTool(workspace)}
}

type bashToolWrapper struct {
	tool types.Tool
}

func (t *bashToolWrapper) Name() string        { return "Bash" }
func (t *bashToolWrapper) Description() string { return t.tool.Description() }
func (t *bashToolWrapper) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The command to execute.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Optional timeout in seconds (max 600).",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Clear, concise description of what this command does.",
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "Run the command in the background; use job_output/job_kill to manage it.",
			},
			// Retained for backwards compatibility with the runtime tool.
			"background": map[string]interface{}{
				"type":        "boolean",
				"description": "Alias for run_in_background.",
			},
		},
		"required": []string{"command"},
	}
}
func (t *bashToolWrapper) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	// Claude Code names the background flag run_in_background; the underlying
	// command tool understands `background`. Translate without mutating input.
	if _, ok := input["run_in_background"]; ok {
		if _, hasBackground := input["background"]; !hasBackground {
			cloned := make(map[string]interface{}, len(input)+1)
			for k, v := range input {
				cloned[k] = v
			}
			cloned["background"] = input["run_in_background"]
			input = cloned
		}
	}
	return t.tool.Execute(ctx, input)
}
func (t *bashToolWrapper) Metadata() types.ToolMetadata {
	md := t.tool.Metadata()
	md.SourceNodeName = "Bash"
	md.Extra = map[string]interface{}{"no_cache": true}
	// Shell commands are barriers in Claude Code's scheduler.
	md.ConcurrencySafe = false
	md.ExecutionMode = types.ExecutionModeSequential
	return md
}

// IsConcurrencySafe delegates to the wrapped tool: read-only commands can run
// in parallel, mutating ones stay barriers.
func (t *bashToolWrapper) IsConcurrencySafe(input map[string]interface{}) bool {
	if p, ok := t.tool.(types.ConcurrencySafeProvider); ok {
		return p.IsConcurrencySafe(input)
	}
	return false
}

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

func (t *deferredMCPTool) Name() string                   { return t.tool.Name() }
func (t *deferredMCPTool) Description() string            { return t.tool.Description() }
func (t *deferredMCPTool) Schema() map[string]interface{} { return t.tool.Schema() }
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
