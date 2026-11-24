package tools

import "testing"

// TestBuiltinToolNamesAligned pins the Claude Code-compatible tool names.
func TestBuiltinToolNamesAligned(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		tool interface{ Name() string }
		want string
	}{
		{NewBashTool(dir), "Bash"},
		{NewGrepTool(dir), "Grep"},
		{NewGlobTool(dir), "Glob"},
		{NewWebFetchTool(), "WebFetch"},
		{NewWebSearchTool(), "WebSearch"},
	}
	for _, tc := range cases {
		if got := tc.tool.Name(); got != tc.want {
			t.Errorf("tool name = %q, want %q", got, tc.want)
		}
	}
}

// TestBashToolSchemaAcceptsClaudeParams verifies the Bash schema exposes the
// Claude Code parameters (description/run_in_background) in addition to the
// command/timeout it always supported.
func TestBashToolSchemaAcceptsClaudeParams(t *testing.T) {
	schema := NewBashTool(t.TempDir()).Schema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema properties = %T", schema["properties"])
	}
	for _, key := range []string{"command", "timeout", "description", "run_in_background"} {
		if _, ok := props[key]; !ok {
			t.Errorf("Bash schema missing %q", key)
		}
	}
}

// TestBashToolNoCache ensures command results never reuse a cached observation.
func TestBashToolNoCache(t *testing.T) {
	md := NewBashTool(t.TempDir()).Metadata()
	if v, _ := md.Extra["no_cache"].(bool); !v {
		t.Fatalf("Bash metadata must set no_cache=true, got %+v", md.Extra)
	}
}
