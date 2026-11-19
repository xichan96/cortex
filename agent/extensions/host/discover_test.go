package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestDiscoverScansSubdirectories(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "beta"), `{"name":"beta","version":"2.0.0","entry":"./bin/beta","capabilities":["tools"]}`)
	writeManifest(t, filepath.Join(root, "alpha"), `{"name":"alpha","version":"1.0.0","entry":"./bin/alpha","args":["--x"],"env":["A=B"],"capabilities":["tools","hooks"]}`)
	writeManifest(t, filepath.Join(root, ".hidden"), `{"name":"hidden","entry":"./hidden"}`)
	if err := os.MkdirAll(filepath.Join(root, "no-manifest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	packages, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(packages) != 2 {
		t.Fatalf("packages = %+v", packages)
	}
	if packages[0].Manifest.Name != "alpha" || packages[1].Manifest.Name != "beta" {
		t.Fatalf("order = %q, %q", packages[0].Manifest.Name, packages[1].Manifest.Name)
	}

	alpha := packages[0]
	if alpha.Manifest.Version != "1.0.0" {
		t.Fatalf("version = %q", alpha.Manifest.Version)
	}
	if alpha.ManifestPath != filepath.Join(root, "alpha", ManifestName) {
		t.Fatalf("manifest path = %q", alpha.ManifestPath)
	}
	entry, err := alpha.EntryPath()
	if err != nil {
		t.Fatalf("EntryPath: %v", err)
	}
	if entry != filepath.Join(root, "alpha", "bin", "alpha") {
		t.Fatalf("entry = %q", entry)
	}
	spec, err := alpha.ExecSpec()
	if err != nil {
		t.Fatalf("ExecSpec: %v", err)
	}
	if spec.Command != entry || spec.Dir != alpha.Dir {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "--x" {
		t.Fatalf("args = %v", spec.Args)
	}
	if len(spec.Env) != 1 || spec.Env[0] != "A=B" {
		t.Fatalf("env = %v", spec.Env)
	}
}

func TestDiscoverPackageRoot(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"solo","version":"0.2.0","entry":"plugin"}`)

	packages, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(packages) != 1 || packages[0].Manifest.Name != "solo" {
		t.Fatalf("packages = %+v", packages)
	}
}

func TestDiscoverManifestFile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"solo","entry":"plugin"}`)

	packages, err := Discover(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(packages) != 1 || packages[0].Dir != dir {
		t.Fatalf("packages = %+v", packages)
	}
}

func TestDiscoverInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"version":"1.0.0"}`)

	_, err := Discover(dir)
	if !errors.Is(err, ErrManifestNoName) {
		t.Fatalf("err = %v, want ErrManifestNoName", err)
	}
}

func TestDiscoverMissingRoot(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Discover succeeded, want error")
	}
}

func TestPackageMissingEntry(t *testing.T) {
	pkg := Package{Dir: t.TempDir(), Manifest: Manifest{Name: "no-entry"}}
	if _, err := pkg.EntryPath(); !errors.Is(err, ErrManifestNoEntry) {
		t.Fatalf("err = %v, want ErrManifestNoEntry", err)
	}
	if _, err := pkg.ExecSpec(); !errors.Is(err, ErrManifestNoEntry) {
		t.Fatalf("ExecSpec err = %v, want ErrManifestNoEntry", err)
	}
}
