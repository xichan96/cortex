package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func helperExecSpec(t *testing.T, mode string, extraEnv ...string) ExecSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env := append(os.Environ(),
		"CORTEX_PLUGIN_HELPER=1",
		"CORTEX_PLUGIN_MODE="+mode,
	)
	env = append(env, extraEnv...)
	return ExecSpec{
		Command: exe,
		Args:    []string{"-test.run=^TestHelperPlugin$"},
		Env:     env,
	}
}

func startHelper(t *testing.T, mode string, opts Options, extraEnv ...string) *Host {
	t.Helper()
	h, err := Start(context.Background(), helperExecSpec(t, mode, extraEnv...), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestStartAndListTools(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	info := h.Info()
	if info.Name != "fake-plugin" || info.Version != "0.1.0" {
		t.Fatalf("Info = %+v", info)
	}
	if info.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol = %d, want %d", info.ProtocolVersion, ProtocolVersion)
	}
	if !info.Capabilities.Tools || !info.Capabilities.Hooks || !info.Capabilities.Resources {
		t.Fatalf("capabilities = %+v", info.Capabilities)
	}

	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "boom" {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Description != "echo the arguments" {
		t.Fatalf("description = %q", tools[0].Description)
	}
	schema := tools[0].Schema()
	if schema["type"] != "object" {
		t.Fatalf("schema = %+v", schema)
	}
}

func TestCallTool(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	result, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode result: %v (%s)", err, result)
	}
	if out.Tool != "echo" {
		t.Fatalf("tool = %q", out.Tool)
	}
	var args map[string]string
	if err := json.Unmarshal(out.Arguments, &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args["message"] != "hi" {
		t.Fatalf("args = %+v", args)
	}
}

func TestCallToolStream(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	var steps []int
	result, err := h.CallToolStream(context.Background(), "echo", json.RawMessage(`{}`), func(update ToolUpdate) error {
		if update.Event != "tools/update" {
			t.Errorf("event = %q", update.Event)
		}
		var data struct {
			Step int `json:"step"`
		}
		if err := json.Unmarshal(update.Data, &data); err != nil {
			return err
		}
		steps = append(steps, data.Step)
		return nil
	})
	if err != nil {
		t.Fatalf("CallToolStream: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("empty result")
	}
	if len(steps) != 2 || steps[0] != 1 || steps[1] != 2 {
		t.Fatalf("steps = %v", steps)
	}
}

func TestCallToolStreamUpdateError(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	want := errors.New("stop streaming")
	_, err := h.CallToolStream(context.Background(), "echo", json.RawMessage(`{}`), func(ToolUpdate) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestEmit(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	blocked, err := h.Emit(context.Background(), "tool_call", EmitGate, json.RawMessage(`{"block":true}`))
	if err != nil {
		t.Fatalf("Emit gate: %v", err)
	}
	if !blocked.Blocked() || blocked.Reason != "blocked by helper" {
		t.Fatalf("gate result = %+v", blocked)
	}

	observed, err := h.Emit(context.Background(), "session_start", EmitNone, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Emit none: %v", err)
	}
	if observed.Decision != DecisionNone || observed.Blocked() {
		t.Fatalf("none result = %+v", observed)
	}
}

func TestListResources(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	resources, err := h.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("resources = %+v", resources)
	}
	if resources[0].Name != "greeting" || resources[0].Kind != "prompt" {
		t.Fatalf("resource = %+v", resources[0])
	}
}

func TestShutdownIdempotent(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close after Shutdown: %v", err)
	}
	if _, err := h.ListTools(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("ListTools after close = %v, want ErrClosed", err)
	}
}

func TestStartProtocolMismatch(t *testing.T) {
	_, err := Start(context.Background(), helperExecSpec(t, "bad-version"), Options{Timeout: 5 * time.Second})
	if !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("err = %v, want ErrUnsupportedProtocol", err)
	}
}

func TestStartImmediateExit(t *testing.T) {
	_, err := Start(context.Background(), helperExecSpec(t, "exit-immediately"), Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("Start succeeded, want error")
	}
}

func TestStartMissingCommand(t *testing.T) {
	if _, err := Start(context.Background(), ExecSpec{}, Options{}); !errors.Is(err, ErrNoCommand) {
		t.Fatalf("err = %v, want ErrNoCommand", err)
	}
}

func TestCallTimeout(t *testing.T) {
	h := startHelper(t, "hang", Options{Timeout: 300 * time.Millisecond})

	start := time.Now()
	_, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestCrashRestart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "crashed")
	h := startHelper(t, "crash-once", Options{Timeout: 5 * time.Second, MaxRestarts: 2},
		"CORTEX_PLUGIN_MARKER="+marker)

	_, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if !errors.Is(err, ErrPluginExited) {
		t.Fatalf("first call err = %v, want ErrPluginExited", err)
	}

	result, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call after restart: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("empty result after restart")
	}
	if h.Info().Name != "fake-plugin" {
		t.Fatalf("info lost after restart: %+v", h.Info())
	}
}

func TestRestartLimit(t *testing.T) {
	h := startHelper(t, "crash-always", Options{Timeout: 5 * time.Second, MaxRestarts: 1})

	if _, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("first call err = %v, want ErrPluginExited", err)
	}
	if _, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); !errors.Is(err, ErrPluginExited) {
		t.Fatalf("second call err = %v, want ErrPluginExited", err)
	}
	if _, err := h.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); !errors.Is(err, ErrRestartLimit) {
		t.Fatalf("third call err = %v, want ErrRestartLimit", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	h, err := Start(context.Background(), helperExecSpec(t, "echo"), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := h.ListTools(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("ListTools = %v, want ErrClosed", err)
	}
}
