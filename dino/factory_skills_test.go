package dino

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeSkill 在 root/<name>/SKILL.md 写一个最小技能。
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + desc + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

// TestSkillsPathsPriority：两个根含同名技能，取 Paths[0] 的版本；不同名技能都保留。
func TestSkillsPathsPriority(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	writeSkill(t, rootA, "dup", "from-root-a")
	writeSkill(t, rootB, "dup", "from-root-b")
	writeSkill(t, rootB, "only-b", "only-in-b")

	cfg := getTestConfig()
	cfg.Skills = SkillsConfig{Paths: []string{rootA, rootB}, AutoLoad: true}

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	byName := map[string]*Skill{}
	for _, s := range factory.GetSkills() {
		byName[s.Name] = s
	}
	if len(byName) != 2 {
		t.Fatalf("应加载 2 个技能（dup 去重 + only-b），got %d: %v", len(byName), keys(byName))
	}
	dup, ok := byName["dup"]
	if !ok {
		t.Fatal("missing skill dup")
	}
	if dup.Description != "from-root-a" {
		t.Fatalf("同名技能应取 Paths[0] 的版本，got description %q", dup.Description)
	}
	if _, ok := byName["only-b"]; !ok {
		t.Fatal("rootB 独有的技能应保留")
	}
}

// TestSkillsDeprecatedPathAppended：同时设置 Paths 和 Path 时，Path 追加在
// Paths 之后（低优先级），同名技能仍取 Paths[0]。
func TestSkillsDeprecatedPathAppended(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	writeSkill(t, rootA, "dup", "from-paths")
	writeSkill(t, rootB, "dup", "from-legacy-path")

	cfg := getTestConfig()
	cfg.Skills = SkillsConfig{Paths: []string{rootA}, Path: rootB, AutoLoad: true}

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	skills := factory.GetSkills()
	if len(skills) != 1 {
		t.Fatalf("同名技能应只保留 1 个，got %d", len(skills))
	}
	if skills[0].Description != "from-paths" {
		t.Fatalf("Paths 应优先于 deprecated Path，got %q", skills[0].Description)
	}
}

// TestSkillsLegacyPathOnly：只设置 deprecated Path 仍正常工作。
func TestSkillsLegacyPathOnly(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "legacy", "legacy-only")

	cfg := getTestConfig()
	cfg.Skills = SkillsConfig{Path: root, AutoLoad: true}

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	skills := factory.GetSkills()
	if len(skills) != 1 || skills[0].Name != "legacy" {
		t.Fatalf("deprecated Path 应仍可加载技能，got %+v", skills)
	}
}

func TestSkillPaths_OrderAndDedupe(t *testing.T) {
	cfg := SkillsConfig{Paths: []string{"a", "b", "a"}, Path: "c"}
	got := cfg.SkillPaths()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func keys(m map[string]*Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
