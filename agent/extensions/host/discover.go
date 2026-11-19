package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestName is the plugin manifest filename looked up in each package
// directory.
const ManifestName = "cortex-plugin.json"

// Discovery errors.
var (
	ErrManifestNotFound = errors.New("pluginhost: " + ManifestName + " not found")
	ErrManifestInvalid  = errors.New("pluginhost: invalid plugin manifest")
	ErrManifestNoName   = errors.New("pluginhost: plugin manifest has no name")
	ErrManifestNoEntry  = errors.New("pluginhost: plugin manifest has no entry")
)

// Manifest is the cortex-plugin.json schema. Only local paths are supported;
// package installation and remote sources are out of scope for this layer.
type Manifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description,omitempty"`
	Entry        string   `json:"entry,omitempty"`
	Args         []string `json:"args,omitempty"`
	Env          []string `json:"env,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Package is a discovered plugin directory and its manifest.
type Package struct {
	// Dir is the package root directory.
	Dir string
	// ManifestPath is Dir/cortex-plugin.json.
	ManifestPath string
	Manifest     Manifest
}

// EntryPath resolves Manifest.Entry relative to Dir.
func (p Package) EntryPath() (string, error) {
	entry := strings.TrimSpace(p.Manifest.Entry)
	if entry == "" {
		return "", fmt.Errorf("%w: %s", ErrManifestNoEntry, p.ManifestPath)
	}
	if filepath.IsAbs(entry) {
		return filepath.Clean(entry), nil
	}
	return filepath.Join(p.Dir, entry), nil
}

// ExecSpec builds the process specification for the package entry. The command
// must already be executable; discovery never installs or builds it.
func (p Package) ExecSpec() (ExecSpec, error) {
	entry, err := p.EntryPath()
	if err != nil {
		return ExecSpec{}, err
	}
	return ExecSpec{
		Command: entry,
		Args:    append([]string(nil), p.Manifest.Args...),
		Env:     append([]string(nil), p.Manifest.Env...),
		Dir:     p.Dir,
	}, nil
}

// LoadManifest reads and validates the manifest at path.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: %s: %v", ErrManifestInvalid, path, err)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return Manifest{}, fmt.Errorf("%w: %s", ErrManifestNoName, path)
	}
	return manifest, nil
}

// Discover scans root for local plugin packages without installing anything.
//
// If root itself contains cortex-plugin.json it is returned as a single
// package. Otherwise every immediate subdirectory with a manifest is returned,
// sorted by manifest name and then path. Hidden directories are skipped.
func Discover(root string) ([]Package, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		dir := filepath.Dir(root)
		if filepath.Base(root) != ManifestName {
			return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, root)
		}
		pkg, err := loadPackage(dir)
		if err != nil {
			return nil, err
		}
		return []Package{pkg}, nil
	}

	rootManifest := filepath.Join(root, ManifestName)
	if _, err := os.Stat(rootManifest); err == nil {
		pkg, err := loadPackage(root)
		if err != nil {
			return nil, err
		}
		return []Package{pkg}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	packages := make([]Package, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		pkg, err := loadPackage(dir)
		if err != nil {
			return nil, err
		}
		packages = append(packages, pkg)
	}
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].Manifest.Name != packages[j].Manifest.Name {
			return packages[i].Manifest.Name < packages[j].Manifest.Name
		}
		return packages[i].Dir < packages[j].Dir
	})
	return packages, nil
}

func loadPackage(dir string) (Package, error) {
	path := filepath.Join(dir, ManifestName)
	manifest, err := LoadManifest(path)
	if err != nil {
		return Package{}, err
	}
	return Package{Dir: dir, ManifestPath: path, Manifest: manifest}, nil
}
