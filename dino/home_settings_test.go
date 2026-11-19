package dino

import (
	"path/filepath"
	"testing"

	"github.com/xichan96/cortex/agent/home"
)

// newApplySettingsEnv builds a home env for root/cwd with project trust always
// granted (or denied), matching what NewDinoFactory would resolve.
func newApplySettingsEnv(t *testing.T, root, cwd, trust string) *homeEnv {
	t.Helper()
	env, err := loadHomeEnv(HomeConfig{Enabled: true, Root: root, CWD: cwd, ProjectTrust: trust})
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	return env
}

func newApplySettingsConfig(root, cwd string) *Config {
	return &Config{
		Home: HomeConfig{
			Enabled:       true,
			ApplySettings: true,
			Root:          root,
			CWD:           cwd,
			ProjectTrust:  "always",
		},
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestApplyHomeSettings_DefaultsOnlyFillEmptyFields(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings.json"),
		`{"default_provider":"anthropic","default_model":"claude-test"}`)
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := newApplySettingsConfig(root, cwd)
	if diags := applyHomeSettings(cfg, env); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.DefaultProvider != "anthropic" || cfg.DefaultModel != "claude-test" {
		t.Fatalf("defaults = %q/%q, want settings values", cfg.DefaultProvider, cfg.DefaultModel)
	}

	explicit := newApplySettingsConfig(root, cwd)
	explicit.DefaultProvider = "openai"
	explicit.DefaultModel = "host-model"
	applyHomeSettings(explicit, env)
	if explicit.DefaultProvider != "openai" || explicit.DefaultModel != "host-model" {
		t.Fatalf("host values overwritten: %q/%q", explicit.DefaultProvider, explicit.DefaultModel)
	}
}

func TestApplyHomeSettings_ToolsModifierSemantics(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings.json"),
		`{"tools":{"default":["read_file","+grep","-bash"]}}`)
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := newApplySettingsConfig(root, cwd)
	applyHomeSettings(cfg, env)
	assertStrings(t, cfg.Tools.Allowed, []string{"read_file", "grep"})

	explicit := newApplySettingsConfig(root, cwd)
	explicit.Tools.Allowed = []string{"bash"}
	applyHomeSettings(explicit, env)
	assertStrings(t, explicit.Tools.Allowed, []string{"bash"})
}

func TestApplyHomeSettings_SessionDir(t *testing.T) {
	root, cwd, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := newApplySettingsConfig(root, cwd)
	cfg.Home.SessionDir = sessionDir
	cfg.Memory.PersistDirectory = defaultMemoryPersistDirectory
	applyHomeSettings(cfg, env)
	if cfg.Memory.PersistDirectory != sessionDir {
		t.Fatalf("persist dir = %q, want session dir %q", cfg.Memory.PersistDirectory, sessionDir)
	}

	hostPath := filepath.Join(t.TempDir(), "host.db")
	explicit := newApplySettingsConfig(root, cwd)
	explicit.Home.SessionDir = sessionDir
	explicit.Memory.PersistDirectory = hostPath
	applyHomeSettings(explicit, env)
	if explicit.Memory.PersistDirectory != hostPath {
		t.Fatalf("host persist dir overwritten: %q", explicit.Memory.PersistDirectory)
	}

	empty := newApplySettingsConfig(root, cwd)
	empty.Home.SessionDir = sessionDir
	applyHomeSettings(empty, env)
	if empty.Memory.PersistDirectory != sessionDir {
		t.Fatalf("empty persist dir = %q, want session dir", empty.Memory.PersistDirectory)
	}
}

func TestApplyHomeSettings_MCPMergeAndTrust(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{
		"shared":{"url":"https://user.example/mcp","headers":{"X-Scope":"user"}},
		"user-only":{"command":"npx","args":["-y","srv"],"env":{"K":"V"}},
		"disabled":{"url":"https://user.example/disabled","enabled":false}
	}}`)
	writeTestFile(t, filepath.Join(cwd, ".cortex", "mcp.json"), `{"mcpServers":{
		"shared":{"url":"https://project.example/mcp","type":"sse"},
		"project-only":{"url":"https://project.example/only"}
	}}`)

	trusted := newApplySettingsEnv(t, root, cwd, "always")
	cfg := newApplySettingsConfig(root, cwd)
	if diags := applyHomeSettings(cfg, trusted); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !cfg.MCP.Enabled {
		t.Fatal("MCP.Enabled = false, want true when servers are discovered")
	}
	if got := cfg.MCP.Servers["shared"]; got.URL != "https://project.example/mcp" || got.Type != "sse" {
		t.Fatalf("project must override user shared server, got %+v", got)
	}
	if got := cfg.MCP.Servers["user-only"]; got.Command != "npx" || got.Type != "stdio" || len(got.Args) != 2 || got.Env["K"] != "V" {
		t.Fatalf("user-only server = %+v", got)
	}
	if _, ok := cfg.MCP.Servers["project-only"]; !ok {
		t.Fatal("trusted project server missing")
	}
	if cfg.MCP.Servers["disabled"].Enabled {
		t.Fatal("inline enabled:false must be preserved")
	}

	untrusted := newApplySettingsEnv(t, root, cwd, "never")
	untrustedCfg := newApplySettingsConfig(root, cwd)
	applyHomeSettings(untrustedCfg, untrusted)
	if got := untrustedCfg.MCP.Servers["shared"]; got.URL != "https://user.example/mcp" {
		t.Fatalf("untrusted shared server = %+v, want user entry", got)
	}
	if _, ok := untrustedCfg.MCP.Servers["project-only"]; ok {
		t.Fatal("untrusted project MCP server must not load")
	}
	if untrustedCfg.MCP.Servers["disabled"].Enabled {
		t.Fatal("explicit enabled:false must survive decoding")
	}

	// Host-declared servers win over the home layer.
	hostCfg := newApplySettingsConfig(root, cwd)
	hostCfg.MCP.Servers = map[string]MCPServerConfig{
		"shared": {URL: "https://host.example/mcp", Enabled: true},
	}
	applyHomeSettings(hostCfg, trusted)
	if got := hostCfg.MCP.Servers["shared"].URL; got != "https://host.example/mcp" {
		t.Fatalf("host server overwritten by home layer: %q", got)
	}
}

func TestApplyHomeSettings_DisabledLeavesConfigUntouched(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings.json"),
		`{"default_provider":"anthropic","default_model":"claude-test","tools":{"default":["grep"]}}`)
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{"srv":{"url":"https://example/mcp"}}}`)
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := &Config{Home: HomeConfig{Enabled: true, Root: root, CWD: cwd}}
	cfg.Home.SessionDir = "/tmp/sessions"
	if diags := applyHomeSettings(cfg, env); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.DefaultProvider != "" || cfg.DefaultModel != "" || cfg.Tools.Allowed != nil ||
		cfg.Memory.PersistDirectory != "" || cfg.MCP.Enabled || len(cfg.MCP.Servers) != 0 {
		t.Fatalf("config mutated with ApplySettings=false: %+v", cfg)
	}
}

func TestApplyHomeSettings_BadMCPJSONOnlyDiagnoses(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings.json"), `{"default_model":"claude-test"}`)
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{`)
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := newApplySettingsConfig(root, cwd)
	diags := applyHomeSettings(cfg, env)
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want one warning", diags)
	}
	if diags[0].Severity != home.SeverityWarning {
		t.Fatalf("severity = %v, want warning", diags[0].Severity)
	}
	// A broken MCP file must not block the settings that did parse.
	if cfg.DefaultModel != "claude-test" {
		t.Fatalf("default model = %q, want settings value despite bad mcp.json", cfg.DefaultModel)
	}
	if cfg.MCP.Enabled || len(cfg.MCP.Servers) != 0 {
		t.Fatalf("bad mcp.json leaked servers: %+v", cfg.MCP)
	}
}

func TestApplyHomeSettings_BadMCPEntryIsSkipped(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{
		"good":{"url":"https://example/mcp"},
		"bad":"not-an-object",
		"empty":{}
	}}`)
	env := newApplySettingsEnv(t, root, cwd, "always")

	cfg := newApplySettingsConfig(root, cwd)
	diags := applyHomeSettings(cfg, env)
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %v, want one per bad entry", diags)
	}
	if _, ok := cfg.MCP.Servers["good"]; !ok {
		t.Fatal("valid server must still load")
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("servers = %v, want only the valid one", cfg.MCP.Servers)
	}
}
