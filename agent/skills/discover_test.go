package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates rel under root (or an absolute path when root is empty)
// and returns the absolute path.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func skillDoc(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
}

func skillNames(skills []Skill) map[string]Skill {
	out := make(map[string]Skill, len(skills))
	for _, s := range skills {
		out[s.Name] = s
	}
	return out
}

func TestDiscoverRecursiveRules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "# project readme without frontmatter\n")
	writeFile(t, root, "alpha/SKILL.md", skillDoc("alpha", "Alpha skill.", "alpha body"))
	// alpha is a skill root: this nested skill must not be discovered.
	writeFile(t, root, "alpha/nested/SKILL.md", skillDoc("nested", "Nested skill.", "nested body"))
	writeFile(t, root, "deep/more/gamma/SKILL.md", skillDoc("gamma", "Gamma skill.", "gamma body"))
	writeFile(t, root, "loose.md", skillDoc("loose", "Loose markdown skill.", "loose body"))
	writeFile(t, root, "ignored/SKILL.md", skillDoc("ignored", "Ignored skill.", "ignored body"))
	writeFile(t, root, ".gitignore", "ignored/\n")
	writeFile(t, root, ".hidden/SKILL.md", skillDoc("hidden", "Hidden skill.", "hidden body"))
	writeFile(t, root, "node_modules/pkg/SKILL.md", skillDoc("dep", "Dependency skill.", "dep body"))

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got := skillNames(res.Skills)
	for _, want := range []string{"alpha", "gamma", "loose"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing skill %q; got %v", want, keys(got))
		}
	}
	for _, unwanted := range []string{"nested", "ignored", "hidden", "dep"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("skill %q should not be discovered", unwanted)
		}
	}
	if len(res.Skills) != 3 {
		t.Errorf("discovered %d skills (%v), want 3", len(res.Skills), keys(got))
	}
	if skill := got["alpha"]; skill.Content != "" {
		t.Errorf("Discover should be metadata-only, Content = %q", skill.Content)
	}
	if !hasDiagnostic(res.Diagnostics, DiagnosticMissingDesc) {
		t.Errorf("expected missing-description diagnostic for README.md, got %v", res.Diagnostics)
	}
	if hasDiagnostic(res.Diagnostics, DiagnosticCollision) {
		t.Errorf("unexpected collision diagnostic: %v", res.Diagnostics)
	}
}

func TestDiscoverIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".ignore", "skip.md\n")
	writeFile(t, root, "kept.md", skillDoc("kept", "Kept skill.", "kept"))
	writeFile(t, root, "skip.md", skillDoc("skipped", "Skipped skill.", "skipped"))

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := skillNames(res.Skills)
	if _, ok := got["kept"]; !ok {
		t.Errorf("kept.md should be discovered; got %v", keys(got))
	}
	if _, ok := got["skipped"]; ok {
		t.Error("skip.md should be ignored by .ignore")
	}
}

func TestDiscoverNegatedIgnorePattern(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.md\n!keep.md\n")
	writeFile(t, root, "keep.md", skillDoc("keep", "Keep me.", "keep"))
	writeFile(t, root, "drop.md", skillDoc("drop", "Drop me.", "drop"))

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := skillNames(res.Skills)
	if _, ok := got["keep"]; !ok {
		t.Errorf("keep.md should be re-included by negation; got %v", keys(got))
	}
	if _, ok := got["drop"]; ok {
		t.Error("drop.md should be ignored")
	}
}

func TestDiscoverFirstRootWinsCollision(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	writeFile(t, root1, "dup/SKILL.md", skillDoc("dup", "First description.", "first"))
	writeFile(t, root2, "dup/SKILL.md", skillDoc("dup", "Second description.", "second"))

	res, err := Discover(context.Background(), []string{root1, root2}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Skills) != 1 {
		t.Fatalf("len(Skills) = %d, want 1", len(res.Skills))
	}
	if res.Skills[0].Description != "First description." {
		t.Errorf("Description = %q, want first root to win", res.Skills[0].Description)
	}
	if countDiagnostics(res.Diagnostics, DiagnosticCollision) != 1 {
		t.Errorf("expected one collision diagnostic, got %v", res.Diagnostics)
	}
}

func TestDiscoverSkipsMissingDescription(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "nodesc/SKILL.md", "---\nname: nodesc\n---\nbody\n")

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Skills) != 0 {
		t.Errorf("expected no skills, got %v", keys(skillNames(res.Skills)))
	}
	if !hasDiagnostic(res.Diagnostics, DiagnosticMissingDesc) {
		t.Errorf("expected missing-description diagnostic, got %v", res.Diagnostics)
	}
}

func TestDiscoverBadFrontmatterDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "broken/SKILL.md", "---\nname: [unclosed\n---\nbody\n")

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Skills) != 0 {
		t.Errorf("expected no skills, got %d", len(res.Skills))
	}
	if !hasDiagnostic(res.Diagnostics, DiagnosticParseError) {
		t.Errorf("expected parse-error diagnostic, got %v", res.Diagnostics)
	}
}

func TestDiscoverOptions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "loose.md", skillDoc("loose", "Loose skill.", "loose"))
	writeFile(t, root, "a/b/c/SKILL.md", skillDoc("deep", "Deep skill.", "deep"))

	res, err := Discover(context.Background(), []string{root}, DiscoverOptions{SkipRootMarkdown: true, MaxDepth: 1})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := skillNames(res.Skills)
	if _, ok := got["loose"]; ok {
		t.Error("loose.md should be skipped with SkipRootMarkdown")
	}
	if _, ok := got["deep"]; ok {
		t.Error("depth-3 skill should be skipped with MaxDepth=1")
	}

	res, err = Discover(context.Background(), []string{root}, DiscoverOptions{MaxDepth: 3})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got = skillNames(res.Skills)
	if _, ok := got["loose"]; !ok {
		t.Errorf("loose.md should be discovered by default; got %v", keys(got))
	}
	if _, ok := got["deep"]; !ok {
		t.Errorf("depth-3 skill should be discovered with MaxDepth=3; got %v", keys(got))
	}
}

func keys(m map[string]Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
