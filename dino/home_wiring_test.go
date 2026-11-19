package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/home"
	agentskills "github.com/xichan96/cortex/agent/skills"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeTestSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: "+description+"\n---\n\n# "+name+"\n")
}

func TestLoadHomeEnv_SystemPromptsContextAndTrust(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	writeTestFile(t, filepath.Join(root, "AGENTS.md"), "global context")
	writeTestFile(t, filepath.Join(root, "SYSTEM.md"), "GLOBAL SYSTEM")
	writeTestFile(t, filepath.Join(root, "APPEND_SYSTEM.md"), "GLOBAL APPEND")
	writeTestFile(t, filepath.Join(root, "skills", "global-skill", "SKILL.md"), "---\nname: global-skill\ndescription: global skill\n---\n")

	writeTestFile(t, filepath.Join(cwd, "AGENTS.md"), "project context")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "SYSTEM.md"), "PROJECT SYSTEM")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "APPEND_SYSTEM.md"), "PROJECT APPEND")
	writeTestFile(t, filepath.Join(cwd, ".cortex", "skills", "project-skill", "SKILL.md"), "---\nname: project-skill\ndescription: project skill\n---\n")

	env, err := loadHomeEnv(HomeConfig{Enabled: true, Root: root, CWD: cwd, ProjectTrust: "always"})
	if err != nil {
		t.Fatalf("loadHomeEnv: %v", err)
	}
	if env.systemPrompt != "PROJECT SYSTEM" {
		t.Errorf("system prompt = %q, want project override", env.systemPrompt)
	}
	if env.appendSystemPrompt != "PROJECT APPEND" {
		t.Errorf("append prompt = %q, want project override", env.appendSystemPrompt)
	}

	var joined strings.Builder
	for _, cf := range env.context {
		joined.WriteString(cf.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "global context") || !strings.Contains(joined.String(), "project context") {
		t.Errorf("context files = %q, want both global and project instructions", joined.String())
	}

	roots := env.skillRoots()
	if !containsString(roots, filepath.Join(root, "skills")) {
		t.Errorf("skill roots = %v, want user skills dir", roots)
	}
	if !containsString(roots, filepath.Join(cwd, ".cortex", "skills")) {
		t.Errorf("skill roots = %v, want trusted project skills dir", roots)
	}

	untrusted, err := loadHomeEnv(HomeConfig{Enabled: true, Root: root, CWD: cwd, ProjectTrust: "never"})
	if err != nil {
		t.Fatalf("loadHomeEnv untrusted: %v", err)
	}
	if untrusted.systemPrompt != "GLOBAL SYSTEM" {
		t.Errorf("untrusted system prompt = %q, want global", untrusted.systemPrompt)
	}
	if containsString(untrusted.skillRoots(), filepath.Join(cwd, ".cortex", "skills")) {
		t.Errorf("untrusted skill roots = %v, must not include project skills", untrusted.skillRoots())
	}
	var untrustedContext strings.Builder
	for _, cf := range untrusted.context {
		untrustedContext.WriteString(cf.Content)
	}
	if !strings.Contains(untrustedContext.String(), "project context") {
		t.Error("context files must load regardless of project trust")
	}
}

func TestComposeSystemPrompt_OrderingAndDedupe(t *testing.T) {
	f := &dinoFactory{
		config: &Config{SystemPrompt: "BASE"},
		home: &homeEnv{
			systemPrompt:       "SYS",
			appendSystemPrompt: "APP",
			context: []home.ContextFile{
				{Path: "/ctx/AGENTS.md", Scope: "project", Content: "CTX"},
				{Path: "/ctx/dup.md", Scope: "user", Content: "SYS"},
			},
		},
		cortexSkills: []*agentskills.Skill{{Name: "s", Description: "d"}},
	}

	got := f.composeSystemPrompt("EXTRA")
	positions := []struct {
		name string
		text string
	}{
		{"sys", "SYS"},
		{"app", "APP"},
		{"ctx", "CTX"},
		{"extra", "EXTRA"},
		{"skills", "## Available Skills"},
	}
	last := -1
	for _, p := range positions {
		idx := strings.Index(got, p.text)
		if idx < 0 {
			t.Fatalf("composed prompt missing %q:\n%s", p.name, got)
		}
		if idx <= last {
			t.Errorf("%s appears out of order (index %d, previous %d):\n%s", p.name, idx, last, got)
		}
		last = idx
	}
	if strings.Count(got, "SYS") != 1 {
		t.Errorf("context file already present in the base should be skipped, got %d SYS occurrences", strings.Count(got, "SYS"))
	}
	if strings.Contains(got, "BASE") {
		t.Error("SYSTEM.md should replace the configured base prompt")
	}
}

func TestNewDinoFactory_HomeSkillsLoaded(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	writeTestSkill(t, filepath.Join(root, "skills", "global-skill"), "global-skill", "global skill")
	writeTestSkill(t, filepath.Join(cwd, ".cortex", "skills", "project-skill"), "project-skill", "project skill")

	cfg := getTestConfig()
	cfg.Home = HomeConfig{Enabled: true, Root: root, CWD: cwd, ProjectTrust: "always"}
	cfg.Skills.AutoLoad = true

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	names := make(map[string]bool)
	for _, s := range factory.GetSkills() {
		names[s.Name] = true
	}
	if !names["global-skill"] || !names["project-skill"] {
		t.Fatalf("loaded skills = %v, want global-skill and project-skill", names)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
