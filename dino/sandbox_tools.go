package dino

// Sandboxed tool wrappers.
//
// These wrappers route the builtin tool surface (bash, Read, Write,
// list_directory) through a sandbox.Sandbox so that path confinement, timeouts,
// output limits and (for future backends) process isolation are enforced in one
// place instead of in each tool. They are additive: nothing here is wired into
// the factory yet, and WrapToolWithSandbox leaves unknown tools untouched.
//
// The wrappers keep the wrapped tool's Name/Description/Schema/Metadata by
// delegating to it. When a wrapper is constructed without an inner tool (for
// standalone use or tests) it falls back to the builtin shape.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/sandbox"
)

// sandboxableToolNames lists the builtin tools that WrapToolWithSandbox knows
// how to route through a Sandbox. Anything else is returned unchanged.
var sandboxableToolNames = map[string]struct{}{
	"bash":           {},
	"Read":           {},
	"Write":          {},
	"list_directory": {},
}

// WrapToolWithSandbox returns a sandboxed equivalent of tool. It recognizes the
// builtin bash/Read/Write/list_directory tools and returns false for anything
// else, in which case the original tool is returned unchanged.
func WrapToolWithSandbox(tool types.Tool, sb sandbox.Sandbox) (types.Tool, bool) {
	if tool == nil || sb == nil {
		return tool, false
	}
	if _, ok := sandboxableToolNames[tool.Name()]; !ok {
		return tool, false
	}
	switch tool.Name() {
	case "bash":
		return NewSandboxedBashTool(tool, sb), true
	case "Read":
		return NewSandboxedReadFileTool(tool, sb), true
	case "Write":
		return NewSandboxedWriteFileTool(tool, sb), true
	case "list_directory":
		return NewSandboxedListDirectoryTool(tool, sb), true
	default:
		return tool, false
	}
}

// wrapperMeta returns the wrapped tool's metadata, or a builtin fallback.
func wrapperMeta(inner types.Tool) types.ToolMetadata {
	if inner != nil {
		return inner.Metadata()
	}
	return types.ToolMetadata{
		SourceNodeName: "sandbox",
		IsFromToolkit:  false,
		ToolType:       "builtin",
		Priority:       10,
	}
}

func wrapperName(inner types.Tool, fallback string) string {
	if inner != nil && inner.Name() != "" {
		return inner.Name()
	}
	return fallback
}

func wrapperDescription(inner types.Tool, fallback string) string {
	if inner != nil {
		return inner.Description()
	}
	return fallback
}

func wrapperSchema(inner types.Tool, fallback map[string]interface{}) map[string]interface{} {
	if inner != nil && inner.Schema() != nil {
		return inner.Schema()
	}
	return fallback
}

// -----------------------------------------------------------------------------
// bash

// SandboxedBashTool executes shell commands through a sandbox.Sandbox.
//
// Accepted inputs, in addition to the wrapped tool's schema:
//
//	command          string, required
//	args             []string, optional; when present command is executed
//	                 directly with these arguments and no shell is involved
//	timeout          number of seconds, optional
//	dir              working directory inside the sandbox root, optional
//	env              object of string values, optional
//	stdin            string fed to standard input, optional
//	max_output_bytes integer per-stream cap, optional
//
// Without args the command string is interpreted by the platform shell
// (/bin/sh -c on Unix, cmd /C on Windows), matching the legacy bash tool. The
// sandbox still confines the working directory and applies its own env policy.
type SandboxedBashTool struct {
	inner types.Tool
	sb    sandbox.Sandbox
}

// NewSandboxedBashTool wraps inner (which may be nil) with the given sandbox.
func NewSandboxedBashTool(inner types.Tool, sb sandbox.Sandbox) *SandboxedBashTool {
	return &SandboxedBashTool{inner: inner, sb: sb}
}

func (t *SandboxedBashTool) Name() string { return wrapperName(t.inner, "bash") }

func (t *SandboxedBashTool) Description() string {
	return wrapperDescription(t.inner, "Execute a command inside the sandbox and return stdout, stderr and the exit code.")
}

func (t *SandboxedBashTool) Schema() map[string]interface{} {
	return wrapperSchema(t.inner, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Command to execute"},
			"args": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional arguments; when set, command is executed directly without a shell",
			},
			"timeout": map[string]interface{}{"type": "integer", "description": "Optional timeout in seconds"},
		},
		"required": []string{"command"},
	})
}

func (t *SandboxedBashTool) Metadata() types.ToolMetadata { return wrapperMeta(t.inner) }

func (t *SandboxedBashTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.sb == nil {
		return nil, fmt.Errorf("sandboxed bash: no sandbox backend")
	}
	command, ok := input["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("invalid 'command' parameter: must be a non-empty string")
	}
	if background, _ := input["background"].(bool); background {
		return nil, fmt.Errorf("background execution is not supported by the sandbox wrapper")
	}

	spec := sandbox.ExecSpec{}
	if rawArgs, exists := input["args"]; exists && rawArgs != nil {
		args, err := stringSliceArg(rawArgs)
		if err != nil {
			return nil, err
		}
		spec.Command = command
		spec.Args = args
	} else {
		spec.Command, spec.Args = sandboxShellCommand(command)
	}
	if dir, ok := input["dir"].(string); ok {
		spec.Dir = dir
	}
	if rawEnv, exists := input["env"]; exists && rawEnv != nil {
		env, err := stringMapArg(rawEnv)
		if err != nil {
			return nil, err
		}
		spec.Env = env
	}
	if stdin, ok := input["stdin"].(string); ok {
		spec.Stdin = []byte(stdin)
	}
	timeout, err := durationSecondsArg(input["timeout"])
	if err != nil {
		return nil, err
	}
	spec.Timeout = timeout
	if n, ok := intArg(input["max_output_bytes"]); ok {
		spec.MaxOutputBytes = n
	}

	res, runErr := t.sb.Run(ctx, spec)

	out := map[string]interface{}{
		"command":   command,
		"exit_code": res.ExitCode,
		"stdout":    string(res.Stdout),
		"stderr":    string(res.Stderr),
	}
	if res.Duration > 0 {
		out["duration_ms"] = res.Duration.Milliseconds()
	}
	if res.Truncated {
		out["truncated"] = true
	}
	if runErr != nil {
		out["error"] = runErr.Error()
		if errors.Is(runErr, sandbox.ErrTimeout) {
			out["timed_out"] = true
		}
		if errors.Is(runErr, sandbox.ErrCanceled) {
			out["canceled"] = true
		}
		// Return the populated result alongside the error so callers can keep
		// the partial output and still test the sentinel with errors.Is.
		return out, runErr
	}
	return out, nil
}

// sandboxShellCommand builds the argv used to interpret a free-form command
// string. It mirrors the legacy bash tool's shell semantics; the sandbox
// backend still owns directory, environment and process confinement.
func sandboxShellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", command}
	}
	return "/bin/sh", []string{"-c", command}
}

// -----------------------------------------------------------------------------
// Read

// SandboxedReadFileTool reads files through a sandbox.Sandbox.
type SandboxedReadFileTool struct {
	inner types.Tool
	sb    sandbox.Sandbox
}

// NewSandboxedReadFileTool wraps inner (which may be nil) with the given sandbox.
func NewSandboxedReadFileTool(inner types.Tool, sb sandbox.Sandbox) *SandboxedReadFileTool {
	return &SandboxedReadFileTool{inner: inner, sb: sb}
}

func (t *SandboxedReadFileTool) Name() string { return wrapperName(t.inner, "Read") }

func (t *SandboxedReadFileTool) Description() string {
	return wrapperDescription(t.inner, "Read a file inside the sandbox. Supports optional line windows via offset/limit.")
}

func (t *SandboxedReadFileTool) Schema() map[string]interface{} {
	return wrapperSchema(t.inner, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string"},
			"offset":    map[string]interface{}{"type": "integer", "description": "1-based starting line number"},
			"limit":     map[string]interface{}{"type": "integer", "description": "Max number of lines to return"},
		},
		"required": []string{"file_path"},
	})
}

func (t *SandboxedReadFileTool) Metadata() types.ToolMetadata { return wrapperMeta(t.inner) }

func (t *SandboxedReadFileTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.sb == nil {
		return nil, fmt.Errorf("sandboxed Read: no sandbox backend")
	}
	path, ok := input["file_path"].(string)
	if !ok || path == "" {
		path, ok = input["path"].(string)
	}
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	data, err := t.sb.ReadFile(ctx, path)
	if err != nil {
		// Preserve the legacy behavior of listing a directory passed to Read,
		// when the backend can enumerate directories.
		if info, statErr := t.sb.Stat(ctx, path); statErr == nil && info.IsDir {
			names, listErr := sandboxListNames(ctx, t.sb, path)
			if listErr != nil {
				return nil, listErr
			}
			return fmt.Sprintf("Directory listing for %s:\n%s", path, strings.Join(names, "\n")), nil
		}
		return nil, fmt.Errorf("sandboxed Read: %w", err)
	}

	return readFileResult(path, string(data), input), nil
}

// readFileResult renders a 1-based line window as "<line>\t<content>", matching
// the builtin Read tool.
func readFileResult(path, content string, input map[string]interface{}) string {
	offset, hasOffset := intArg(input["offset"])
	limit, hasLimit := intArg(input["limit"])
	if !hasOffset || offset < 1 {
		offset = 1
	}
	if !hasLimit || limit <= 0 {
		limit = 2000
	}

	lines := strings.Split(content, "\n")
	total := len(lines)
	if total > 0 && lines[total-1] == "" {
		total--
	}

	start := offset - 1
	if start > total {
		start = total
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

// -----------------------------------------------------------------------------
// Write

// SandboxedWriteFileTool writes files through a sandbox.Sandbox.
type SandboxedWriteFileTool struct {
	inner types.Tool
	sb    sandbox.Sandbox
}

// NewSandboxedWriteFileTool wraps inner (which may be nil) with the given sandbox.
func NewSandboxedWriteFileTool(inner types.Tool, sb sandbox.Sandbox) *SandboxedWriteFileTool {
	return &SandboxedWriteFileTool{inner: inner, sb: sb}
}

func (t *SandboxedWriteFileTool) Name() string { return wrapperName(t.inner, "Write") }

func (t *SandboxedWriteFileTool) Description() string {
	return wrapperDescription(t.inner, "Write a file inside the sandbox, creating parent directories as needed.")
}

func (t *SandboxedWriteFileTool) Schema() map[string]interface{} {
	return wrapperSchema(t.inner, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string"},
			"content":   map[string]interface{}{"type": "string"},
		},
		"required": []string{"file_path", "content"},
	})
}

func (t *SandboxedWriteFileTool) Metadata() types.ToolMetadata { return wrapperMeta(t.inner) }

func (t *SandboxedWriteFileTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.sb == nil {
		return nil, fmt.Errorf("sandboxed Write: no sandbox backend")
	}
	path, ok := input["file_path"].(string)
	if !ok || path == "" {
		path, ok = input["path"].(string)
	}
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	content, ok := input["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}
	existed := false
	if _, statErr := t.sb.Stat(ctx, path); statErr == nil {
		existed = true
	}
	// A zero mode lets the backend apply its default file mode.
	if err := t.sb.WriteFile(ctx, path, []byte(content), 0); err != nil {
		return nil, fmt.Errorf("sandboxed Write: %w", err)
	}
	if existed {
		return fmt.Sprintf("The file %s has been updated successfully.", path), nil
	}
	return fmt.Sprintf("File created successfully at: %s", path), nil
}

// -----------------------------------------------------------------------------
// list_directory

// SandboxedListDirectoryTool lists directories through a sandbox backend that
// implements sandbox.DirLister.
type SandboxedListDirectoryTool struct {
	inner types.Tool
	sb    sandbox.Sandbox
}

// NewSandboxedListDirectoryTool wraps inner (which may be nil) with the given
// sandbox.
func NewSandboxedListDirectoryTool(inner types.Tool, sb sandbox.Sandbox) *SandboxedListDirectoryTool {
	return &SandboxedListDirectoryTool{inner: inner, sb: sb}
}

func (t *SandboxedListDirectoryTool) Name() string { return wrapperName(t.inner, "list_directory") }

func (t *SandboxedListDirectoryTool) Description() string {
	return wrapperDescription(t.inner, "List files in a directory inside the sandbox.")
}

func (t *SandboxedListDirectoryTool) Schema() map[string]interface{} {
	return wrapperSchema(t.inner, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
		"required": []string{"path"},
	})
}

func (t *SandboxedListDirectoryTool) Metadata() types.ToolMetadata { return wrapperMeta(t.inner) }

func (t *SandboxedListDirectoryTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.sb == nil {
		return nil, fmt.Errorf("sandboxed list_directory: no sandbox backend")
	}
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("invalid path parameter")
	}
	return sandboxListEntries(ctx, t.sb, path)
}

// sandboxListNames returns entry names, appending "/" to directories, matching
// the legacy Read directory listing.
func sandboxListNames(ctx context.Context, sb sandbox.Sandbox, path string) ([]string, error) {
	entries, err := sandboxReadDir(ctx, sb, path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, info := range entries {
		if info.IsDir {
			names = append(names, info.Name+"/")
		} else {
			names = append(names, info.Name)
		}
	}
	return names, nil
}

// sandboxListEntries returns the legacy list_directory result shape.
func sandboxListEntries(ctx context.Context, sb sandbox.Sandbox, path string) (interface{}, error) {
	entries, err := sandboxReadDir(ctx, sb, path)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(entries))
	for _, info := range entries {
		result = append(result, map[string]interface{}{
			"name":     info.Name,
			"is_dir":   info.IsDir,
			"size":     info.Size,
			"mode":     info.Mode.String(),
			"mod_time": info.ModTime.Unix(),
		})
	}
	return result, nil
}

func sandboxReadDir(ctx context.Context, sb sandbox.Sandbox, path string) ([]sandbox.FileInfo, error) {
	lister, ok := sb.(sandbox.DirLister)
	if !ok {
		return nil, fmt.Errorf("sandbox backend %q does not support directory listing", sb.Name())
	}
	entries, err := lister.ReadDir(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("sandboxed list_directory: %w", err)
	}
	return entries, nil
}

// -----------------------------------------------------------------------------
// input helpers

func stringSliceArg(v interface{}) ([]string, error) {
	switch args := v.(type) {
	case []string:
		return args, nil
	case []interface{}:
		out := make([]string, len(args))
		for i, item := range args {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("'args[%d]' must be a string", i)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("'args' must be an array of strings")
	}
}

func stringMapArg(v interface{}) (map[string]string, error) {
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]string, len(m))
		for k, item := range m {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("'env[%q]' must be a string", k)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("'env' must be an object of strings")
	}
}

func durationSecondsArg(v interface{}) (time.Duration, error) {
	if v == nil {
		return 0, nil
	}
	seconds, ok := numericArg(v)
	if !ok {
		return 0, fmt.Errorf("invalid 'timeout': must be a number of seconds")
	}
	if seconds < 0 {
		return 0, fmt.Errorf("invalid 'timeout': must not be negative")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func intArg(v interface{}) (int, bool) {
	n, ok := numericArg(v)
	if !ok {
		return 0, false
	}
	return int(n), true
}

func numericArg(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
