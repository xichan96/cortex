package home

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filterContextFiles keeps only files under the given directories so assertions
// do not depend on AGENTS.md files that may exist on the host above t.TempDir().
func filterContextFiles(files []ContextFile, dirs ...string) []ContextFile {
	out := make([]ContextFile, 0, len(files))
	for _, f := range files {
		canonicalFile := CanonicalPath(f.Path)
		for _, dir := range dirs {
			canonicalDir := CanonicalPath(dir)
			if canonicalFile == canonicalDir ||
				strings.HasPrefix(canonicalFile, canonicalDir+string(os.PathSeparator)) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func contextTestPaths(t *testing.T, home, cwd string) Paths {
	t.Helper()
	p, err := Resolve(Options{Home: home, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadContextFilesOrder(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	mid := filepath.Join(root, "a")
	cwd := filepath.Join(mid, "b")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(home, "AGENTS.md"), "global")
	writeTestFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeTestFile(t, filepath.Join(mid, "CORTEX.md"), "mid")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.md"), "cwd-base")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.override.md"), "cwd-override")

	allFiles, diags, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatalf("LoadContextFiles: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	files := filterContextFiles(allFiles, home, root)

	var got []string
	for _, f := range files {
		got = append(got, f.Content)
	}
	want := []string{"global", "root", "mid", "cwd-override"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}

	if files[0].Scope != "user" {
		t.Errorf("global scope = %q, want user", files[0].Scope)
	}
	for _, f := range files[1:] {
		if f.Scope != "project" {
			t.Errorf("%s scope = %q, want project", f.Path, f.Scope)
		}
	}
}

func TestContextPerDirectoryOnlyOneFile(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeTestFile(t, filepath.Join(cwd, "CLAUDE.md"), "claude")
	writeTestFile(t, filepath.Join(cwd, "CORTEX.md"), "cortex")

	allFiles, _, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatal(err)
	}
	files := filterContextFiles(allFiles, cwd)
	var contents []string
	for _, f := range files {
		contents = append(contents, f.Content)
	}
	if len(contents) != 1 || contents[0] != "cortex" {
		t.Fatalf("contents = %v, want only CORTEX.md (higher precedence than CLAUDE.md)", contents)
	}
}

func TestContextCandidatePrecedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeTestFile(t, filepath.Join(cwd, "CLAUDE.md"), "claude")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.MD"), "agents-upper")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.md"), "agents")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.override.md"), "override")

	allFiles, _, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatal(err)
	}
	files := filterContextFiles(allFiles, cwd)
	if len(files) != 1 || files[0].Content != "override" {
		t.Fatalf("contents = %+v, want AGENTS.override.md", files)
	}
}

func TestContextStripsBOM(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	path := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...), 0o644); err != nil {
		t.Fatal(err)
	}

	allFiles, _, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatal(err)
	}
	files := filterContextFiles(allFiles, cwd)
	if len(files) != 1 || files[0].Content != "hello" {
		t.Fatalf("BOM not stripped: %+v", files)
	}
}

func TestContextSkipsDirectoriesAndUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	home := t.TempDir()
	cwd := t.TempDir()

	// A directory named like a candidate is not a file: fall through.
	if err := os.Mkdir(filepath.Join(cwd, "AGENTS.override.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(cwd, "AGENTS.md")
	writeTestFile(t, unreadable, "secret")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })
	writeTestFile(t, filepath.Join(cwd, "CORTEX.md"), "fallback")

	allFiles, diags, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatalf("unreadable context file must not fail loading: %v", err)
	}
	files := filterContextFiles(allFiles, cwd)
	if len(files) != 1 || files[0].Content != "fallback" {
		t.Fatalf("files = %+v, want CORTEX.md fallback", files)
	}
	if diagnosticsWithSeverity(diags, SeverityWarning) == 0 {
		t.Fatalf("expected a warning diagnostic, got %+v", diags)
	}
}

func TestContextDeduplicatesSymlinkedFile(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	cwd := filepath.Join(root, "nested")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(root, "AGENTS.md")
	writeTestFile(t, rootFile, "shared")
	if err := os.Symlink(rootFile, filepath.Join(cwd, "AGENTS.override.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, diags, err := LoadContextFiles(context.Background(), contextTestPaths(t, home, cwd))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range files {
		if f.Content == "shared" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("shared content loaded %d times, want once: %+v", count, files)
	}
	if diagnosticsWithSeverity(diags, SeverityInfo) == 0 {
		t.Fatalf("expected a dedupe info diagnostic, got %+v", diags)
	}
}

func TestContextEmptyCWD(t *testing.T) {
	if _, _, err := LoadContextFiles(context.Background(), Paths{}); err == nil {
		t.Fatal("expected an error for an empty CWD")
	}
}

func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	home := t.TempDir()
	cwd := t.TempDir()
	if _, _, err := LoadContextFiles(ctx, contextTestPaths(t, home, cwd)); err == nil {
		t.Fatal("expected a cancellation error")
	}
}
