package dino

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/sandbox"
)

// stubSandboxTool is a minimal types.Tool used to exercise the wrappers without
// depending on the real builtin implementations.
type stubSandboxTool struct {
	name   string
	desc   string
	schema map[string]interface{}
	meta   types.ToolMetadata
}

func (s *stubSandboxTool) Name() string                   { return s.name }
func (s *stubSandboxTool) Description() string            { return s.desc }
func (s *stubSandboxTool) Schema() map[string]interface{} { return s.schema }
func (s *stubSandboxTool) Metadata() types.ToolMetadata   { return s.meta }
func (s *stubSandboxTool) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return "inner-result", nil
}

func newSandboxToolHost(t *testing.T, opts sandbox.HostOptions) *sandbox.HostSandbox {
	t.Helper()
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	sb, err := sandbox.NewHostSandbox(opts)
	if err != nil {
		t.Fatalf("NewHostSandbox: %v", err)
	}
	return sb
}

func requireSandboxShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires a Unix shell")
	}
}

func sandboxOutput(t *testing.T, res interface{}) map[string]interface{} {
	t.Helper()
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("result type = %T, want map[string]interface{}", res)
	}
	return m
}

func TestSandboxToolBashEcho(t *testing.T) {
	requireSandboxShell(t)
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	wrapped, ok := WrapToolWithSandbox(&stubSandboxTool{name: "Bash"}, sb)
	if !ok {
		t.Fatal("WrapToolWithSandbox(Bash) = false")
	}
	res, err := wrapped.Execute(context.Background(), map[string]interface{}{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := sandboxOutput(t, res)
	if got := out["stdout"]; got != "hello\n" {
		t.Fatalf("stdout = %q, want %q", got, "hello\n")
	}
	if got := out["exit_code"]; got != 0 {
		t.Fatalf("exit_code = %v, want 0", got)
	}
	if got := out["stderr"]; got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestSandboxToolBashArgsBypassShell(t *testing.T) {
	requireSandboxShell(t)
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	tool := NewSandboxedBashTool(nil, sb)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo",
		"args":    []interface{}{"one", "two"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := sandboxOutput(t, res)
	if got := out["stdout"]; got != "one two\n" {
		t.Fatalf("stdout = %q, want %q", got, "one two\n")
	}
}

func TestSandboxToolBashExitCodeIsNotAnError(t *testing.T) {
	requireSandboxShell(t)
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	tool := NewSandboxedBashTool(nil, sb)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "exit 3"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := sandboxOutput(t, res)["exit_code"]; got != 3 {
		t.Fatalf("exit_code = %v, want 3", got)
	}
}

func TestSandboxToolBashRunsInRoot(t *testing.T) {
	requireSandboxShell(t)
	root := t.TempDir()
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: root})
	tool := NewSandboxedBashTool(nil, sb)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "pwd"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimSpace(sandboxOutput(t, res)["stdout"].(string))
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestSandboxToolReadWriteList(t *testing.T) {
	root := t.TempDir()
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: root})

	writeTool, ok := WrapToolWithSandbox(&stubSandboxTool{name: "Write"}, sb)
	if !ok {
		t.Fatal("WrapToolWithSandbox(Write) = false")
	}
	if _, err := writeTool.Execute(context.Background(), map[string]interface{}{
		"file_path": "nested/hello.txt",
		"content":   "line1\nline2\nline3\n",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "hello.txt")); err != nil {
		t.Fatalf("written file missing: %v", err)
	}

	readTool, ok := WrapToolWithSandbox(&stubSandboxTool{name: "Read"}, sb)
	if !ok {
		t.Fatal("WrapToolWithSandbox(Read) = false")
	}
	res, err := readTool.Execute(context.Background(), map[string]interface{}{"file_path": "nested/hello.txt"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	wantRead := "1\tline1\n2\tline2\n3\tline3"
	if res != wantRead {
		t.Fatalf("Read = %q, want %q", res, wantRead)
	}

	// Line window is honored because the wrapped schema advertises it.
	res, err = readTool.Execute(context.Background(), map[string]interface{}{
		"file_path": "nested/hello.txt",
		"offset":    float64(2),
		"limit":     float64(1),
	})
	if err != nil {
		t.Fatalf("Read window: %v", err)
	}
	if !strings.HasPrefix(res.(string), "2\tline2") || !strings.Contains(res.(string), "Use offset=3") {
		t.Fatalf("window = %q, want line 2 plus continuation hint", res)
	}

	entries, err := sandboxListNames(context.Background(), sb, "nested")
	if err != nil {
		t.Fatalf("sandboxListNames: %v", err)
	}
	if len(entries) != 1 || entries[0] != "hello.txt" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestSandboxToolRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: root})

	readTool := NewSandboxedReadFileTool(nil, sb)
	if _, err := readTool.Execute(context.Background(), map[string]interface{}{"path": "../outside.txt"}); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("read escape error = %v, want ErrPathEscape", err)
	}

	writeTool := NewSandboxedWriteFileTool(nil, sb)
	if _, err := writeTool.Execute(context.Background(), map[string]interface{}{
		"path":    "../outside.txt",
		"content": "nope",
	}); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("write escape error = %v, want ErrPathEscape", err)
	}

	if _, err := sandboxListNames(context.Background(), sb, ".."); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("list escape error = %v, want ErrPathEscape", err)
	}
}

func TestSandboxToolTimeoutPassThrough(t *testing.T) {
	requireSandboxShell(t)
	sb := newSandboxToolHost(t, sandbox.HostOptions{
		Root:           t.TempDir(),
		DefaultTimeout: 100 * time.Millisecond,
	})
	tool := NewSandboxedBashTool(nil, sb)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "sleep 5"})
	if !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	out := sandboxOutput(t, res)
	if out["timed_out"] != true {
		t.Fatalf("timed_out = %v, want true", out["timed_out"])
	}
	if out["exit_code"] != -1 {
		t.Fatalf("exit_code = %v, want -1", out["exit_code"])
	}
}

func TestSandboxToolTruncationPassThrough(t *testing.T) {
	requireSandboxShell(t)
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	tool := NewSandboxedBashTool(nil, sb)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":          "echo",
		"args":             []interface{}{"abcdefghij"},
		"max_output_bytes": float64(4),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := sandboxOutput(t, res)
	if out["truncated"] != true {
		t.Fatalf("truncated = %v, want true", out["truncated"])
	}
	if got := out["stdout"]; got != "abcd" {
		t.Fatalf("stdout = %q, want %q", got, "abcd")
	}
}

func TestWrapToolWithSandboxUnknownTool(t *testing.T) {
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	original := &stubSandboxTool{name: "WebSearch", desc: "search"}
	got, ok := WrapToolWithSandbox(original, sb)
	if ok {
		t.Fatal("WrapToolWithSandbox(WebSearch) = true, want false")
	}
	if got != original {
		t.Fatal("unknown tool was replaced")
	}

	if got, ok := WrapToolWithSandbox(original, nil); ok || got != original {
		t.Fatal("nil sandbox should leave the tool unchanged")
	}
	if got, ok := WrapToolWithSandbox(nil, sb); ok || got != nil {
		t.Fatal("nil tool should return nil, false")
	}
}

func TestSandboxToolPreservesInnerMetadata(t *testing.T) {
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	schema := map[string]interface{}{"type": "object", "title": "custom"}
	meta := types.ToolMetadata{SourceNodeName: "custom", ToolType: "builtin", Priority: 42}
	inner := &stubSandboxTool{name: "Read", desc: "custom read", schema: schema, meta: meta}

	wrapped, ok := WrapToolWithSandbox(inner, sb)
	if !ok {
		t.Fatal("WrapToolWithSandbox(Read) = false")
	}
	if got := wrapped.Name(); got != "Read" {
		t.Fatalf("Name() = %q", got)
	}
	if got := wrapped.Description(); got != "custom read" {
		t.Fatalf("Description() = %q", got)
	}
	if got := wrapped.Schema(); got["title"] != "custom" {
		t.Fatalf("Schema() = %v", got)
	}
	if got := wrapped.Metadata(); got.Priority != 42 || got.SourceNodeName != "custom" {
		t.Fatalf("Metadata() = %+v", got)
	}
}

func TestSandboxToolFallbackNameAndSchema(t *testing.T) {
	sb := newSandboxToolHost(t, sandbox.HostOptions{Root: t.TempDir()})
	cases := []struct {
		tool types.Tool
		name string
	}{
		{NewSandboxedBashTool(nil, sb), "Bash"},
		{NewSandboxedReadFileTool(nil, sb), "Read"},
		{NewSandboxedWriteFileTool(nil, sb), "Write"},
	}
	for _, tc := range cases {
		if got := tc.tool.Name(); got != tc.name {
			t.Errorf("Name() = %q, want %q", got, tc.name)
		}
		if tc.tool.Description() == "" {
			t.Errorf("%s: empty Description", tc.name)
		}
		if tc.tool.Schema() == nil {
			t.Errorf("%s: nil Schema", tc.name)
		}
	}
}

func TestSandboxToolRequiresBackend(t *testing.T) {
	tools := []types.Tool{
		NewSandboxedBashTool(nil, nil),
		NewSandboxedReadFileTool(nil, nil),
		NewSandboxedWriteFileTool(nil, nil),
	}
	for _, tool := range tools {
		if _, err := tool.Execute(context.Background(), map[string]interface{}{"path": "x", "command": "x", "content": "x"}); err == nil {
			t.Errorf("%s: expected error without a backend", tool.Name())
		}
	}
}
