package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentskills "github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
)

// writeLazySkill writes root/<dir>/SKILL.md with optional extra frontmatter
// lines. dir and name usually match; they can differ to exercise the
// name-vs-directory rule.
func writeLazySkill(t *testing.T, root, dir, name, description, extra, body string) string {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	doc := "---\nname: " + name + "\n"
	if description != "" {
		doc += "description: " + description + "\n"
	}
	doc += extra
	doc += "---\n\n" + body
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func newSkillsFactory(t *testing.T, roots ...string) DinoFactory {
	t.Helper()
	cfg := getTestConfig()
	cfg.Skills = SkillsConfig{Paths: roots, AutoLoad: true}
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Shutdown(context.Background()) })
	return factory
}

func findSkillTool(t *testing.T, factory DinoFactory, name string) types.Tool {
	t.Helper()
	for _, tool := range factory.GetTools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}

func executeSkillTool(t *testing.T, tool types.Tool, input map[string]interface{}) (string, error) {
	t.Helper()
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		return "", err
	}
	s, ok := out.(string)
	if !ok {
		t.Fatalf("skill tool returned %T, want string", out)
	}
	return s, nil
}

// TestSkillsLazy_StartupIndexesMetadataOnly pins the core P2-A contract:
// startup parses metadata only, bodies are not read, and the system prompt
// carries the index (with location) but not the body.
func TestSkillsLazy_StartupIndexesMetadataOnly(t *testing.T) {
	root := t.TempDir()
	path := writeLazySkill(t, root, "alpha", "alpha", "Alpha skill.", "", "# SKILL-BODY-ALPHA\n")

	factory := newSkillsFactory(t, root)

	skills := factory.GetSkills()
	if len(skills) != 1 {
		t.Fatalf("GetSkills() = %d, want 1", len(skills))
	}
	if skills[0].Name != "alpha" || skills[0].Description != "Alpha skill." {
		t.Fatalf("unexpected metadata: %+v", skills[0])
	}
	if skills[0].Prompt != "" {
		t.Errorf("GetSkills() must be metadata-only, got Prompt %q", skills[0].Prompt)
	}

	idx := factory.SkillIndex()
	if idx == nil {
		t.Fatal("SkillIndex() is nil")
	}
	entry, ok := idx.Get("alpha")
	if !ok {
		t.Fatal("alpha missing from index")
	}
	if entry.Path != path && !strings.HasSuffix(entry.Path, "SKILL.md") {
		t.Errorf("entry.Path = %q, want SKILL.md", entry.Path)
	}
	if !strings.HasSuffix(entry.BaseDir, "alpha") {
		t.Errorf("entry.BaseDir = %q, want it to end with alpha", entry.BaseDir)
	}

	prompt := factory.(*dinoFactory).skillIndexPrompt()
	if !strings.Contains(prompt, "<available_skills>") || !strings.Contains(prompt, "<name>alpha</name>") {
		t.Fatalf("system prompt index missing skill:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<location>") || !strings.Contains(prompt, "SKILL.md") {
		t.Errorf("system prompt index should advertise the location:\n%s", prompt)
	}
	if strings.Contains(prompt, "SKILL-BODY-ALPHA") {
		t.Errorf("skill body must not be in the system prompt:\n%s", prompt)
	}

	// Changing the body without reloading proves startup never read it: the
	// very first LoadBody sees the new content.
	writeLazySkill(t, root, "alpha", "alpha", "Alpha skill.", "", "# SKILL-BODY-ALPHA-EDITED\n")
	body, err := idx.LoadBody("alpha")
	if err != nil {
		t.Fatalf("LoadBody: %v", err)
	}
	if !strings.Contains(body, "SKILL-BODY-ALPHA-EDITED") {
		t.Errorf("LoadBody did not read the current body: %q", body)
	}
}

// TestLazySkillTool_ReturnsBodyAndBaseDir covers the lazy tool: body on
// demand, location/baseDir hints, appended input, case-insensitive names, and
// recoverable errors for unknown skills and unreadable files.
func TestLazySkillTool_ReturnsBodyAndBaseDir(t *testing.T) {
	root := t.TempDir()
	writeLazySkill(t, root, "alpha", "alpha", "Alpha skill.", "", "# SKILL-BODY-ALPHA\n")
	factory := newSkillsFactory(t, root)
	tool := findSkillTool(t, factory, "skill")

	if meta := tool.Metadata(); meta.ToolType != "builtin" || meta.Priority != 5 {
		t.Errorf("metadata = %+v, want builtin/5", meta)
	}

	out, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "alpha"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"Skill: alpha", "Location:", "SKILL.md", "relative paths", "SKILL-BODY-ALPHA", `<skill name="alpha"`} {
		if !strings.Contains(out, want) {
			t.Errorf("skill output missing %q:\n%s", want, out)
		}
	}

	out, err = executeSkillTool(t, tool, map[string]interface{}{"skill_name": "ALPHA", "input": "summarize it"})
	if err != nil {
		t.Fatalf("Execute (case-insensitive): %v", err)
	}
	if !strings.Contains(out, "<args>summarize it</args>") {
		t.Errorf("input should be appended as <args>:\n%s", out)
	}

	if _, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "missing"}); err == nil {
		t.Fatal("unknown skill must return an error")
	} else if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the skill: %v", err)
	}

	// Deleting the file makes LoadBody fail; the tool must return a
	// recoverable error rather than panicking or returning stale content.
	if err := os.Remove(filepath.Join(root, "alpha", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "alpha"}); err == nil {
		t.Fatal("unreadable skill file must return an error")
	}
}

// TestReloadSkillsPicksUpChanges covers hot reload: added, deleted and edited
// skills are reflected by the index and LoadBody after factory.ReloadSkills.
func TestReloadSkillsPicksUpChanges(t *testing.T) {
	root := t.TempDir()
	writeLazySkill(t, root, "alpha", "alpha", "Alpha skill.", "", "# ALPHA-ONE\n")
	factory := newSkillsFactory(t, root)
	ctx := context.Background()

	if _, err := factory.SkillIndex().LoadBody("alpha"); err != nil {
		t.Fatalf("initial LoadBody: %v", err)
	}

	// Edited body.
	writeLazySkill(t, root, "alpha", "alpha", "Alpha skill.", "", "# ALPHA-TWO\n")
	// New skill.
	writeLazySkill(t, root, "beta", "beta", "Beta skill.", "", "# BETA-BODY\n")

	if err := factory.ReloadSkills(ctx); err != nil {
		t.Fatalf("ReloadSkills: %v", err)
	}
	if _, ok := factory.SkillIndex().Get("beta"); !ok {
		t.Error("beta missing after reload")
	}
	body, err := factory.SkillIndex().LoadBody("alpha")
	if err != nil {
		t.Fatalf("LoadBody after reload: %v", err)
	}
	if !strings.Contains(body, "ALPHA-TWO") {
		t.Errorf("edited body not picked up: %q", body)
	}

	// Deleted skill disappears from both GetSkills and the index/tool.
	if err := os.RemoveAll(filepath.Join(root, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := factory.ReloadSkills(ctx); err != nil {
		t.Fatalf("ReloadSkills after delete: %v", err)
	}
	for _, s := range factory.GetSkills() {
		if s.Name == "alpha" {
			t.Error("deleted skill still listed by GetSkills")
		}
	}
	tool := findSkillTool(t, factory, "skill")
	if _, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "alpha"}); err == nil {
		t.Error("deleted skill should no longer load")
	}
}

// TestSkillsLazy_Diagnostics covers name collisions and missing descriptions:
// they are reported through the index diagnostics instead of aborting startup.
func TestSkillsLazy_Diagnostics(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	writeLazySkill(t, rootA, "dup", "dup", "from-root-a", "", "# A\n")
	writeLazySkill(t, rootB, "dup", "dup", "from-root-b", "", "# B\n")
	writeLazySkill(t, rootA, "nodesc", "nodesc", "", "", "# NODESC\n")

	factory := newSkillsFactory(t, rootA, rootB)
	idx := factory.SkillIndex()

	if !hasSkillDiagnostic(idx.Diagnostics(), agentskills.DiagnosticCollision) {
		t.Errorf("expected collision diagnostic, got %v", idx.Diagnostics())
	}
	if !hasSkillDiagnostic(idx.Diagnostics(), agentskills.DiagnosticMissingDesc) {
		t.Errorf("expected missing-description diagnostic, got %v", idx.Diagnostics())
	}
	if _, ok := idx.Get("nodesc"); ok {
		t.Error("skill without description must be skipped")
	}
	entry, ok := idx.Get("dup")
	if !ok {
		t.Fatal("dup missing from index")
	}
	if entry.Description != "from-root-a" {
		t.Errorf("first root should win the collision, got %q", entry.Description)
	}
}

func hasSkillDiagnostic(diags []agentskills.Diagnostic, kind string) bool {
	for _, d := range diags {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

// TestSkillsLazy_ConstraintsVisible covers requirement 4: allowed_tools and
// paths are advertised to the model and readable by the host.
func TestSkillsLazy_ConstraintsVisible(t *testing.T) {
	root := t.TempDir()
	writeLazySkill(t, root, "pdf", "pdf", "PDF tools.",
		"allowed-tools: [read, write]\npaths: [\"**/*.pdf\"]\ndisable-model-invocation: false\n",
		"# PDF-BODY\n")
	writeLazySkill(t, root, "private", "private", "Hidden skill.",
		"disable-model-invocation: true\n", "# PRIVATE-BODY\n")

	factory := newSkillsFactory(t, root)
	idx := factory.SkillIndex()

	entry, ok := idx.Get("pdf")
	if !ok {
		t.Fatal("pdf missing from index")
	}
	if got := entry.AllowedTools(); len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Errorf("entry.AllowedTools() = %v", got)
	}
	if got := entry.Paths(); len(got) != 1 || got[0] != "**/*.pdf" {
		t.Errorf("entry.Paths() = %v", got)
	}

	prompt := factory.(*dinoFactory).skillIndexPrompt()
	for _, want := range []string{
		"<allowed_tools>read, write</allowed_tools>",
		"<paths>**/*.pdf</paths>",
		"<name>pdf</name>",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "<name>private</name>") {
		t.Errorf("disable-model-invocation skill must stay out of the prompt:\n%s", prompt)
	}

	// Host-visible constraints through GetSkills.
	var hostSkill *Skill
	for _, s := range factory.GetSkills() {
		if s.Name == "pdf" {
			hostSkill = s
		}
	}
	if hostSkill == nil {
		t.Fatal("pdf missing from GetSkills")
	}
	if len(hostSkill.AllowedTools) != 2 || len(hostSkill.Paths) != 1 {
		t.Errorf("GetSkills constraints = %+v", hostSkill)
	}
	if tools, ok := factory.(*dinoFactory).SkillAllowedTools("pdf"); !ok || len(tools) != 2 {
		t.Errorf("SkillAllowedTools(pdf) = %v, %v", tools, ok)
	}
	if _, ok := factory.(*dinoFactory).SkillAllowedTools("private"); ok {
		t.Error("private declares no allowed-tools, expected ok=false")
	}

	// Tool return exposes the constraints too.
	out, err := executeSkillTool(t, findSkillTool(t, factory, "skill"), map[string]interface{}{"skill_name": "pdf"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Allowed tools: read, write") || !strings.Contains(out, "Paths: **/*.pdf") {
		t.Errorf("tool output should expose constraints:\n%s", out)
	}

	// disable-model-invocation only blocks advertising; explicit use works.
	privateOut, err := executeSkillTool(t, findSkillTool(t, factory, "skill"), map[string]interface{}{"skill_name": "private"})
	if err != nil {
		t.Fatalf("explicit private skill: %v", err)
	}
	if !strings.Contains(privateOut, "PRIVATE-BODY") {
		t.Errorf("private body missing:\n%s", privateOut)
	}
}

// TestRestrictToolNames covers the allowed_tools filtering helper. Round-level
// enforcement lives in the engine; this pins the helper it will use.
func TestRestrictToolNames(t *testing.T) {
	available := []string{"read", "write", "Bash"}
	if got := RestrictToolNames(available, []string{"Bash", "read"}); len(got) != 2 || got[0] != "read" || got[1] != "Bash" {
		t.Errorf("RestrictToolNames = %v, want [read bash] (available order)", got)
	}
	if got := RestrictToolNames(available, nil); len(got) != 3 {
		t.Errorf("empty allowed-tools means no restriction, got %v", got)
	}
	// Unknown allowed names are ignored (they are warned about at startup).
	if got := RestrictToolNames(available, []string{"read", "does-not-exist"}); len(got) != 1 || got[0] != "read" {
		t.Errorf("RestrictToolNames with unknown entry = %v", got)
	}
	// The returned slice is a copy.
	got := RestrictToolNames(available, nil)
	got[0] = "mutated"
	if available[0] != "read" {
		t.Error("RestrictToolNames must not alias the input slice")
	}
}
