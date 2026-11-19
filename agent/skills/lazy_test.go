package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTestIndex(t *testing.T, roots ...string) *Index {
	t.Helper()
	ix, _, err := LoadIndex(context.Background(), roots, DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	return ix
}

func TestLoadIndexMetadataOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pdf/SKILL.md", "---\nname: pdf\ndescription: PDF skill.\nallowed-tools: [read]\n---\n\n# PDF body\n")

	ix := loadTestIndex(t, root)
	entry, ok := ix.Get("pdf")
	if !ok {
		t.Fatal("pdf not in index")
	}
	if entry.Description != "PDF skill." {
		t.Errorf("Description = %q", entry.Description)
	}
	if entry.BaseDir != filepath.Join(realpathKey(root), "pdf") {
		t.Errorf("BaseDir = %q, want %q", entry.BaseDir, filepath.Join(realpathKey(root), "pdf"))
	}
	if !strings.HasSuffix(entry.Path, "SKILL.md") {
		t.Errorf("Path = %q", entry.Path)
	}
	if entry.Spec.License != "" || len(entry.Spec.AllowedTools) != 1 {
		t.Errorf("unexpected spec: %+v", entry.Spec)
	}
	// Metadata-only conversion has no body.
	if skill := entry.Skill(); skill.Content != "" {
		t.Errorf("Skill().Content = %q, want empty", skill.Content)
	}
	if len(ix.All()) != 1 {
		t.Errorf("All() = %d entries", len(ix.All()))
	}
}

func TestLoadBodyAndCache(t *testing.T) {
	root := t.TempDir()
	path := writeFile(t, root, "pdf/SKILL.md", "---\nname: pdf\ndescription: PDF skill.\n---\n\nBody one.\n")
	ix := loadTestIndex(t, root)

	body, err := ix.LoadBody("pdf")
	if err != nil {
		t.Fatalf("LoadBody: %v", err)
	}
	if body != "Body one." {
		t.Errorf("body = %q", body)
	}

	// The cache is keyed by mtime+size; changing the size must invalidate it.
	if err := os.WriteFile(path, []byte("---\nname: pdf\ndescription: PDF skill.\n---\n\nBody two, longer.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err = ix.LoadBody("pdf")
	if err != nil {
		t.Fatalf("LoadBody after change: %v", err)
	}
	if body != "Body two, longer." {
		t.Errorf("body after change = %q", body)
	}

	// A cache hit must not need to re-read the file.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if body, err = ix.LoadBody("pdf"); err != nil {
		t.Fatalf("cached LoadBody should not read the file: %v", err)
	}
	if body != "Body two, longer." {
		t.Errorf("cached body = %q", body)
	}

	ix.Invalidate("pdf")
	if _, err := ix.LoadBody("pdf"); err == nil {
		t.Fatal("LoadBody after Invalidate should fail to read an unreadable file")
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	ix.InvalidateAll()
	if body, err = ix.LoadBody("pdf"); err != nil {
		t.Fatalf("LoadBody after InvalidateAll: %v", err)
	}
	if body != "Body two, longer." {
		t.Errorf("body = %q", body)
	}
}

func TestLoadBodyUnknownSkill(t *testing.T) {
	ix := loadTestIndex(t, t.TempDir())
	if _, err := ix.LoadBody("missing"); err == nil {
		t.Fatal("expected an error for an unknown skill")
	}
}

func TestIndexReload(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "alpha/SKILL.md", skillDoc("alpha", "Alpha.", "alpha"))
	ix := loadTestIndex(t, root)
	if _, ok := ix.Get("alpha"); !ok {
		t.Fatal("alpha missing after initial load")
	}

	writeFile(t, root, "beta/SKILL.md", skillDoc("beta", "Beta.", "beta"))
	if _, err := ix.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := ix.Get("beta"); !ok {
		t.Error("beta missing after Reload")
	}

	if err := os.RemoveAll(filepath.Join(root, "alpha")); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := ix.Get("alpha"); ok {
		t.Error("alpha should be removed after Reload")
	}
}

func TestIndexAdvertisableSkipsDisableModelInvocation(t *testing.T) {
	ix := loadTestIndex(t, "testdata")

	var names []string
	for _, entry := range ix.Advertisable() {
		names = append(names, entry.Name)
	}
	if containsString(names, "private-skill") {
		t.Errorf("private-skill must not be advertisable: %v", names)
	}
	if !containsString(names, "pdf-tools") {
		t.Errorf("pdf-tools should be advertisable: %v", names)
	}

	prompt := ix.FormatAvailableSkills()
	if strings.Contains(prompt, "private-skill") {
		t.Errorf("available_skills prompt should omit private-skill:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<available_skills>") || !strings.Contains(prompt, "<name>pdf-tools</name>") {
		t.Errorf("unexpected available_skills prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<location>") {
		t.Errorf("available_skills prompt should include locations:\n%s", prompt)
	}
}

func TestIndexDiagnosticsExposed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "nodesc/SKILL.md", "---\nname: nodesc\n---\nbody\n")
	ix, diags, err := LoadIndex(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if !hasDiagnostic(diags, DiagnosticMissingDesc) {
		t.Errorf("expected missing-description diagnostic, got %v", diags)
	}
	if !hasDiagnostic(ix.Diagnostics(), DiagnosticMissingDesc) {
		t.Errorf("Index.Diagnostics() = %v", ix.Diagnostics())
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
