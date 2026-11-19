package dino

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/home"
)

func resourceByName(resources []Resource, name string) (Resource, bool) {
	for _, r := range resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

func hasDiagnostic(diags []home.Diagnostic, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.Error(), substr) {
			return true
		}
	}
	return false
}

func TestHomeResources_PromptsAndAgentsProjectOverride(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "prompts", "review.md"), "user review")
	writeTestFile(t, filepath.Join(root, "prompts", "plan.md"), "user plan")
	writeTestFile(t, filepath.Join(root, "agents", "helper.md"), "user helper")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "prompts", "review.md"), "project review")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "agents", "helper.md"), "project helper")

	env := newApplySettingsEnv(t, root, cwd, "always")
	res := env.Resources

	review, ok := resourceByName(res.Prompts, "review")
	if !ok {
		t.Fatalf("prompts = %v, want review", res.Prompts)
	}
	if review.Scope != ResourceScopeProject || review.Path != filepath.Join(cwd, ".cortex", "prompts", "review.md") {
		t.Errorf("review = %+v, want trusted project prompt", review)
	}
	plan, ok := resourceByName(res.Prompts, "plan")
	if !ok {
		t.Fatalf("prompts = %v, want plan", res.Prompts)
	}
	if plan.Scope != ResourceScopeUser || plan.Path != filepath.Join(root, "prompts", "plan.md") {
		t.Errorf("plan = %+v, want user prompt", plan)
	}
	if len(res.Prompts) != 2 {
		t.Errorf("prompts = %v, want exactly user+project merge without duplicates", res.Prompts)
	}
	if res.Prompts[0].Name != "review" || res.Prompts[0].Scope != ResourceScopeProject {
		t.Errorf("prompts[0] = %+v, want project entry first", res.Prompts[0])
	}

	helper, ok := resourceByName(res.Agents, "helper")
	if !ok {
		t.Fatalf("agents = %v, want helper", res.Agents)
	}
	if helper.Scope != ResourceScopeProject || helper.Path != filepath.Join(cwd, ".cortex", "agents", "helper.md") {
		t.Errorf("helper = %+v, want project agent", helper)
	}
	if len(res.Agents) != 1 {
		t.Errorf("agents = %v, want only the project override", res.Agents)
	}

	if !hasDiagnostic(res.Diagnostics, "overridden by project resource") {
		t.Errorf("diagnostics = %v, want override warning", res.Diagnostics)
	}
}

func TestHomeResources_PluginManifests(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "plugins", "acme", "cortex-pack.json"),
		`{"name":"acme-tools","version":"1.2.0"}`)
	writeTestFile(t, filepath.Join(root, "plugins", "plug", "plugin.json"),
		`{"name":"plug-b","version":"0.1.0"}`)
	writeTestFile(t, filepath.Join(root, "plugins", "pkg", "package.json"),
		`{"name":"pkg-c","version":"2.0.0"}`)
	writeTestFile(t, filepath.Join(root, "plugins", "multi", "plugin.json"),
		`{"name":"wrong-name","version":"0.0.1"}`)
	writeTestFile(t, filepath.Join(root, "plugins", "multi", "cortex-pack.json"),
		`{"name":"multi-pack","version":"3.0.0"}`)
	if err := os.MkdirAll(filepath.Join(root, "plugins", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A project plugin whose manifest name collides with a user plugin wins.
	writeTestFile(t, filepath.Join(cwd, ".cortex", "plugins", "project-acme", "plugin.json"),
		`{"name":"acme-tools","version":"9.9.9"}`)

	env := newApplySettingsEnv(t, root, cwd, "always")
	res := env.Resources

	acme, ok := resourceByName(res.Plugins, "acme-tools")
	if !ok {
		t.Fatalf("plugins = %v, want acme-tools", res.Plugins)
	}
	if acme.Scope != ResourceScopeProject || acme.Version != "9.9.9" {
		t.Errorf("acme = %+v, want project override version 9.9.9", acme)
	}
	if acme.Path != filepath.Join(cwd, ".cortex", "plugins", "project-acme") {
		t.Errorf("acme path = %q, want project plugin dir", acme.Path)
	}
	if acme.Manifest != filepath.Join(cwd, ".cortex", "plugins", "project-acme", "plugin.json") {
		t.Errorf("acme manifest = %q, want plugin.json", acme.Manifest)
	}

	for name, wantVersion := range map[string]string{"plug-b": "0.1.0", "pkg-c": "2.0.0", "multi-pack": "3.0.0"} {
		plugin, ok := resourceByName(res.Plugins, name)
		if !ok {
			t.Fatalf("plugins = %v, want %s", res.Plugins, name)
		}
		if plugin.Version != wantVersion {
			t.Errorf("%s version = %q, want %q", name, plugin.Version, wantVersion)
		}
		if plugin.Scope != ResourceScopeUser {
			t.Errorf("%s scope = %q, want user", name, plugin.Scope)
		}
	}

	bare, ok := resourceByName(res.Plugins, "bare")
	if !ok {
		t.Fatalf("plugins = %v, want bare directory recorded despite missing manifest", res.Plugins)
	}
	if bare.Version != "" || bare.Manifest != "" {
		t.Errorf("bare = %+v, want no manifest metadata", bare)
	}
	if !hasDiagnostic(res.Diagnostics, "has no manifest") {
		t.Errorf("diagnostics = %v, want missing-manifest warning", res.Diagnostics)
	}
}

func TestHomeResources_Models(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		root, cwd := t.TempDir(), t.TempDir()
		writeTestFile(t, filepath.Join(root, "models.json"),
			`{"anthropic":{"models":{"claude-test":{"context_window":200000}}}}`)

		env := newApplySettingsEnv(t, root, cwd, "always")
		providers, ok := env.Resources.Models["anthropic"].(map[string]any)
		if !ok {
			t.Fatalf("models = %v, want parsed anthropic entry", env.Resources.Models)
		}
		models, ok := providers["models"].(map[string]any)
		if !ok {
			t.Fatalf("anthropic = %v, want models map", providers)
		}
		if _, ok := models["claude-test"]; !ok {
			t.Errorf("models = %v, want claude-test", models)
		}
		if hasDiagnostic(env.Resources.Diagnostics, "models.json") {
			t.Errorf("diagnostics = %v, want none for a valid file", env.Resources.Diagnostics)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		root, cwd := t.TempDir(), t.TempDir()
		writeTestFile(t, filepath.Join(root, "models.json"), `{"anthropic":`)

		env, err := loadHomeEnv(HomeConfig{Enabled: true, Root: root, CWD: cwd, ProjectTrust: "always"})
		if err != nil {
			t.Fatalf("loadHomeEnv must not fail on a broken models.json: %v", err)
		}
		if env.Resources.Models != nil {
			t.Errorf("models = %v, want nil for broken JSON", env.Resources.Models)
		}
		if !hasDiagnostic(env.Resources.Diagnostics, "parse models.json") {
			t.Errorf("diagnostics = %v, want parse warning", env.Resources.Diagnostics)
		}
	})

	t.Run("missing", func(t *testing.T) {
		root, cwd := t.TempDir(), t.TempDir()
		env := newApplySettingsEnv(t, root, cwd, "always")
		if env.Resources.Models != nil {
			t.Errorf("models = %v, want nil when models.json is absent", env.Resources.Models)
		}
		if hasDiagnostic(env.Resources.Diagnostics, "models.json") {
			t.Errorf("diagnostics = %v, want none for a missing file", env.Resources.Diagnostics)
		}
	})
}

func TestHomeResources_UntrustedSkipsProject(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(root, "prompts", "review.md"), "user review")
	writeTestFile(t, filepath.Join(root, "agents", "user-agent.md"), "user agent")
	writeTestFile(t, filepath.Join(root, "plugins", "user-plugin", "plugin.json"),
		`{"name":"user-plugin","version":"1.0.0"}`)
	writeTestFile(t, filepath.Join(cwd, ".cortex", "prompts", "review.md"), "project review")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "agents", "project-agent.md"), "project agent")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "plugins", "project-plugin", "plugin.json"),
		`{"name":"project-plugin","version":"1.0.0"}`)

	env := newApplySettingsEnv(t, root, cwd, "never")
	res := env.Resources

	review, ok := resourceByName(res.Prompts, "review")
	if !ok {
		t.Fatalf("prompts = %v, want user review", res.Prompts)
	}
	if review.Scope != ResourceScopeUser {
		t.Errorf("review = %+v, want user scope when project is untrusted", review)
	}
	if len(res.Prompts) != 1 || len(res.Agents) != 1 || len(res.Plugins) != 1 {
		t.Errorf("resources = %d prompts/%d agents/%d plugins, want only the user layer",
			len(res.Prompts), len(res.Agents), len(res.Plugins))
	}
	if _, ok := resourceByName(res.Agents, "project-agent"); ok {
		t.Errorf("agents = %v, must not include project agents", res.Agents)
	}
	if _, ok := resourceByName(res.Plugins, "project-plugin"); ok {
		t.Errorf("plugins = %v, must not include project plugins", res.Plugins)
	}
	if res.MemoryProjectDir != "" {
		t.Errorf("memory project dir = %q, want empty when untrusted", res.MemoryProjectDir)
	}
	if hasDiagnostic(res.Diagnostics, "overridden by project resource") {
		t.Errorf("diagnostics = %v, no project override is possible when untrusted", res.Diagnostics)
	}
}

func TestHomeResources_MemoryAndAuthPaths(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	// Auth files are recorded as locations only; their contents must never be read.
	writeTestFile(t, filepath.Join(root, "auth.json"), `{"api_key":"super-secret"}`)

	env := newApplySettingsEnv(t, root, cwd, "always")
	res := env.Resources

	if res.MemoryUserDir != filepath.Join(root, "memory") {
		t.Errorf("memory user dir = %q, want %q", res.MemoryUserDir, filepath.Join(root, "memory"))
	}
	if res.MemoryProjectDir != filepath.Join(cwd, ".cortex", "memory") {
		t.Errorf("memory project dir = %q, want %q", res.MemoryProjectDir, filepath.Join(cwd, ".cortex", "memory"))
	}
	if res.AuthPath != filepath.Join(root, "auth.json") {
		t.Errorf("auth path = %q, want %q", res.AuthPath, filepath.Join(root, "auth.json"))
	}
	if res.MCPAuthPath != filepath.Join(root, "mcp-auth.json") {
		t.Errorf("mcp auth path = %q, want %q", res.MCPAuthPath, filepath.Join(root, "mcp-auth.json"))
	}
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Error(), "super-secret") {
			t.Fatalf("diagnostic leaked auth content: %s", d.Error())
		}
	}
}
