package dino

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/home"
)

// trustFixture writes a workspace with project-provided executable resources
// (settings, skills, SYSTEM.md) plus an AGENTS.md context file, all under a
// temporary agent home and working directory.
type trustFixture struct {
	root string
	cwd  string
}

func newTrustFixture(t *testing.T) *trustFixture {
	t.Helper()
	return &trustFixture{root: t.TempDir(), cwd: t.TempDir()}
}

func (f *trustFixture) writeProjectResources(t *testing.T) {
	t.Helper()
	writeTestFile(t, filepath.Join(f.root, "SYSTEM.md"), "GLOBAL SYSTEM")
	writeTestFile(t, filepath.Join(f.cwd, "AGENTS.md"), "project context")
	writeTestFile(t, filepath.Join(f.cwd, ".cortex", "settings.json"), `{"default_model":"project-model"}`)
	writeTestFile(t, filepath.Join(f.cwd, ".cortex", "SYSTEM.md"), "PROJECT SYSTEM")
	writeTestFile(t, filepath.Join(f.cwd, ".cortex", "skills", "project-skill", "SKILL.md"),
		"---\nname: project-skill\ndescription: project skill\n---\n")
}

// writeTrustJSON writes <root>/trust.json with a decision for the fixture CWD.
// Keys are canonical paths, matching FileTrustStore lookup.
func (f *trustFixture) writeTrustJSON(t *testing.T, allowed bool) {
	t.Helper()
	data, err := json.Marshal(map[string]bool{home.CanonicalPath(f.cwd): allowed})
	if err != nil {
		t.Fatalf("marshal trust.json: %v", err)
	}
	writeTestFile(t, filepath.Join(f.root, "trust.json"), string(data))
}

func (f *trustFixture) config() HomeConfig {
	return HomeConfig{Enabled: true, Root: f.root, CWD: f.cwd, ProjectTrust: "ask"}
}

func requireProjectLoaded(t *testing.T, env *homeEnv) {
	t.Helper()
	if !env.trusted {
		t.Fatal("env.trusted = false, want trusted")
	}
	if env.systemPrompt != "PROJECT SYSTEM" {
		t.Errorf("system prompt = %q, want project override", env.systemPrompt)
	}
	if !containsString(env.skillRoots(), filepath.Join(env.paths.CWD, ".cortex", "skills")) {
		t.Errorf("skill roots = %v, want project skills dir", env.skillRoots())
	}
	if got := env.resolved.Sources["default_model"]; got != home.LayerProject {
		t.Errorf("default_model source = %v, want project layer", got)
	}
	if env.resolved.Settings.DefaultModel != "project-model" {
		t.Errorf("default_model = %q, want project-model", env.resolved.Settings.DefaultModel)
	}
}

func requireProjectSkipped(t *testing.T, env *homeEnv) {
	t.Helper()
	if env.trusted {
		t.Fatal("env.trusted = true, want untrusted")
	}
	if env.systemPrompt != "GLOBAL SYSTEM" {
		t.Errorf("system prompt = %q, want global SYSTEM.md", env.systemPrompt)
	}
	if containsString(env.skillRoots(), filepath.Join(env.paths.CWD, ".cortex", "skills")) {
		t.Errorf("skill roots = %v, must not include project skills", env.skillRoots())
	}
	if got := env.resolved.Sources["default_model"]; got == home.LayerProject {
		t.Errorf("default_model source = project, want project layer skipped")
	}
}

func requireProjectContext(t *testing.T, env *homeEnv) {
	t.Helper()
	var joined strings.Builder
	for _, cf := range env.context {
		joined.WriteString(cf.Content)
	}
	if !strings.Contains(joined.String(), "project context") {
		t.Errorf("context files = %q, want project AGENTS.md regardless of trust", joined.String())
	}
}

func TestLoadHomeEnv_TrustMissingJSONAskSkipsProjectResources(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)

	env, err := loadHomeEnv(f.config())
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectSkipped(t, env)
	requireProjectContext(t, env)
}

func TestLoadHomeEnv_TrustJSONAllowedLoadsProjectResources(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)
	f.writeTrustJSON(t, true)

	env, err := loadHomeEnv(f.config())
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectLoaded(t, env)
	requireProjectContext(t, env)
}

func TestLoadHomeEnv_TrustJSONDeniedSkipsProjectResources(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)
	f.writeTrustJSON(t, false)

	cfg := f.config()
	// A stored denial wins over the default policy.
	cfg.ProjectTrust = "always"

	env, err := loadHomeEnv(cfg)
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectSkipped(t, env)
	requireProjectContext(t, env)
}

func TestLoadHomeEnv_TrustOverrideAllowed(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)
	f.writeTrustJSON(t, false)

	cfg := f.config()
	cfg.TrustOverride = home.TrustAllowed

	env, err := loadHomeEnv(cfg)
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectLoaded(t, env)
}

func TestLoadHomeEnv_TrustHandlerAllowed(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)

	var seen home.TrustRequest
	cfg := f.config()
	cfg.ProjectTrust = "never" // handler must win over the default policy
	cfg.TrustHandler = func(ctx context.Context, req home.TrustRequest) (home.TrustDecision, bool, error) {
		seen = req
		return home.TrustAllowed, true, nil
	}

	env, err := loadHomeEnv(cfg)
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectLoaded(t, env)
	if seen.Mode != home.TrustModeNonInteractive {
		t.Errorf("handler mode = %v, want non-interactive", seen.Mode)
	}
	if seen.CWD != f.cwd {
		t.Errorf("handler cwd = %q, want %q", seen.CWD, f.cwd)
	}
	if !containsString(seen.Reasons, filepath.Join(f.cwd, ".cortex", "SYSTEM.md")) {
		t.Errorf("handler reasons = %v, want existing project SYSTEM.md listed", seen.Reasons)
	}
}

func TestLoadHomeEnv_TrustHandlerDenied(t *testing.T) {
	f := newTrustFixture(t)
	f.writeProjectResources(t)
	f.writeTrustJSON(t, true) // handler must win over a stored allow

	cfg := f.config()
	cfg.TrustHandler = func(ctx context.Context, req home.TrustRequest) (home.TrustDecision, bool, error) {
		return home.TrustDenied, true, nil
	}

	env, err := loadHomeEnv(cfg)
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	requireProjectSkipped(t, env)
}

func TestLoadHomeEnv_ContextFilesIgnoreTrust(t *testing.T) {
	cases := []struct {
		name    string
		allowed bool
	}{
		{name: "denied", allowed: false},
		{name: "allowed", allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTrustFixture(t)
			f.writeProjectResources(t)
			f.writeTrustJSON(t, tc.allowed)

			env, err := loadHomeEnv(f.config())
			if err != nil {
				t.Fatalf("loadHomeEnv: %v", err)
			}
			requireProjectContext(t, env)
		})
	}
}
