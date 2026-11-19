package dino

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/home"
)

// Resource scopes mirror home.ContextFile.Scope.
const (
	ResourceScopeUser    = "user"
	ResourceScopeProject = "project"
)

// Resource is one discovered agent-home resource.
//
// Prompts and Agents point at individual *.md files. Plugins point at their
// installation directory; Version and Manifest are only populated for plugins.
type Resource struct {
	Name  string
	Path  string
	Scope string
	// Version comes from the plugin manifest and is empty when absent.
	Version string
	// Manifest is the plugin manifest path. It is empty when the plugin
	// directory has no cortex-pack.json / plugin.json / package.json.
	Manifest string
}

// HomeResources is the resource snapshot discovered from the agent home and,
// when the project is trusted, <cwd>/.cortex.
//
// It is additive to homeEnv: nothing consumes it yet, so resource discovery
// cannot change session behavior. Secrets are never read: AuthPath and
// MCPAuthPath are recorded as locations only.
type HomeResources struct {
	// Prompts and Agents are priority-ordered: project entries come first,
	// then user entries whose name is not overridden. Duplicate names produce a
	// warning diagnostic.
	Prompts []Resource
	Agents  []Resource
	// Plugins follows the same priority order. Directories without a manifest
	// are still recorded (Name falls back to the directory name) with a warning.
	Plugins []Resource

	MemoryUserDir string
	// MemoryProjectDir is empty unless the project is trusted.
	MemoryProjectDir string

	// Models is the parsed ~/.cortex/models.json. A missing file yields nil; a
	// malformed file yields nil plus a warning diagnostic.
	Models map[string]any

	AuthPath    string
	MCPAuthPath string

	Diagnostics []home.Diagnostic
}

// loadHomeResources discovers prompts, agents, plugins, models, memory roots,
// and auth locations for the resolved layout. It never fails: missing
// directories are skipped silently and unreadable/broken files become warning
// diagnostics on the returned snapshot.
//
// Project resources are only discovered when trusted. Discovery only looks at
// the current cwd's .cortex directory — there is no upward search.
func loadHomeResources(paths home.Paths, trusted bool) HomeResources {
	var res HomeResources
	res.MemoryUserDir = paths.MemoryDir
	res.AuthPath = paths.AuthPath
	res.MCPAuthPath = paths.MCPAuthPath

	userPrompts, diags := discoverMarkdownResources(paths.PromptsDir, ResourceScopeUser)
	res.Diagnostics = append(res.Diagnostics, diags...)
	userAgents, diags := discoverMarkdownResources(paths.AgentsDir, ResourceScopeUser)
	res.Diagnostics = append(res.Diagnostics, diags...)
	userPlugins, diags := discoverPlugins(paths.PluginsDir, ResourceScopeUser)
	res.Diagnostics = append(res.Diagnostics, diags...)

	var projectPrompts, projectAgents, projectPlugins []Resource
	if trusted {
		res.MemoryProjectDir = paths.ProjectMemoryDir

		projectPrompts, diags = discoverMarkdownResources(paths.ProjectPromptsDir, ResourceScopeProject)
		res.Diagnostics = append(res.Diagnostics, diags...)
		projectAgents, diags = discoverMarkdownResources(paths.ProjectAgentsDir, ResourceScopeProject)
		res.Diagnostics = append(res.Diagnostics, diags...)
		projectPlugins, diags = discoverPlugins(paths.ProjectPluginsDir, ResourceScopeProject)
		res.Diagnostics = append(res.Diagnostics, diags...)
	}

	res.Prompts, diags = mergeResources(userPrompts, projectPrompts)
	res.Diagnostics = append(res.Diagnostics, diags...)
	res.Agents, diags = mergeResources(userAgents, projectAgents)
	res.Diagnostics = append(res.Diagnostics, diags...)
	res.Plugins, diags = mergeResources(userPlugins, projectPlugins)
	res.Diagnostics = append(res.Diagnostics, diags...)

	res.Models, diags = loadHomeModels(paths.ModelsPath)
	res.Diagnostics = append(res.Diagnostics, diags...)
	return res
}

// discoverMarkdownResources lists *.md files in dir. Names are the file base
// name without the extension. A missing directory is not an error (most homes
// do not populate every resource kind); an unreadable one is a warning.
func discoverMarkdownResources(dir, scope string) ([]Resource, []home.Diagnostic) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []home.Diagnostic{{
			File:     dir,
			Layer:    resourceScopeLayer(scope),
			Severity: home.SeverityWarning,
			Reason:   "read resource directory: " + err.Error(),
		}}
	}

	resources := make([]Resource, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		resources = append(resources, Resource{
			Name:  strings.TrimSuffix(name, filepath.Ext(name)),
			Path:  filepath.Join(dir, name),
			Scope: scope,
		})
	}
	return resources, nil
}

// pluginManifestFiles are the accepted manifests, in precedence order.
var pluginManifestFiles = []string{"cortex-pack.json", "plugin.json", "package.json"}

// pluginManifest is the subset of a plugin manifest dino currently understands.
type pluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// discoverPlugins treats every non-hidden subdirectory of dir as a plugin.
// The first manifest in pluginManifestFiles supplies name/version; a directory
// without any manifest is still recorded, with a warning.
func discoverPlugins(dir, scope string) ([]Resource, []home.Diagnostic) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []home.Diagnostic{{
			File:     dir,
			Layer:    resourceScopeLayer(scope),
			Severity: home.SeverityWarning,
			Reason:   "read plugin directory: " + err.Error(),
		}}
	}

	layer := resourceScopeLayer(scope)
	resources := make([]Resource, 0, len(entries))
	var diags []home.Diagnostic
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		pluginDir := filepath.Join(dir, name)
		res := Resource{Name: name, Path: pluginDir, Scope: scope}

		manifestPath := findPluginManifest(pluginDir)
		if manifestPath == "" {
			diags = append(diags, home.Diagnostic{
				File:     pluginDir,
				Key:      name,
				Layer:    layer,
				Severity: home.SeverityWarning,
				Reason:   "plugin directory has no manifest (cortex-pack.json, plugin.json, or package.json)",
			})
			resources = append(resources, res)
			continue
		}

		res.Manifest = manifestPath
		manifest, manifestDiags := readPluginManifest(manifestPath, scope)
		diags = append(diags, manifestDiags...)
		if manifest != nil {
			if manifest.Name != "" {
				res.Name = manifest.Name
			}
			res.Version = manifest.Version
		}
		resources = append(resources, res)
	}
	return resources, diags
}

func findPluginManifest(pluginDir string) string {
	for _, name := range pluginManifestFiles {
		path := filepath.Join(pluginDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// readPluginManifest parses one manifest. A broken manifest is a warning, not
// a fatal error: the plugin is still recorded with its directory name.
func readPluginManifest(path, scope string) (*pluginManifest, []home.Diagnostic) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []home.Diagnostic{{
			File:     path,
			Layer:    resourceScopeLayer(scope),
			Severity: home.SeverityWarning,
			Reason:   "read plugin manifest: " + err.Error(),
		}}
	}
	var manifest pluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, []home.Diagnostic{{
			File:     path,
			Layer:    resourceScopeLayer(scope),
			Severity: home.SeverityWarning,
			Reason:   "parse plugin manifest: " + err.Error(),
		}}
	}
	return &manifest, nil
}

// mergeResources applies project-over-user precedence by Name. The result is
// project entries first (source order, already filesystem-sorted), then the
// user entries that were not overridden. A shadowed user entry produces an
// informational warning naming both paths.
func mergeResources(user, project []Resource) ([]Resource, []home.Diagnostic) {
	if len(project) == 0 {
		return user, nil
	}
	userByName := make(map[string]Resource, len(user))
	for _, r := range user {
		userByName[r.Name] = r
	}

	var diags []home.Diagnostic
	overridden := make(map[string]struct{}, len(project))
	for _, p := range project {
		userRes, ok := userByName[p.Name]
		if !ok {
			continue
		}
		overridden[p.Name] = struct{}{}
		diags = append(diags, home.Diagnostic{
			File:     userRes.Path,
			Key:      p.Name,
			Layer:    home.LayerProject,
			Severity: home.SeverityWarning,
			Reason:   "overridden by project resource " + p.Path,
		})
	}

	merged := make([]Resource, 0, len(project)+len(user))
	merged = append(merged, project...)
	for _, r := range user {
		if _, ok := overridden[r.Name]; ok {
			continue
		}
		merged = append(merged, r)
	}
	return merged, diags
}

// loadHomeModels parses <agent-dir>/models.json. It records locations only for
// auth files and never reads them.
func loadHomeModels(path string) (map[string]any, []home.Diagnostic) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []home.Diagnostic{{
			File:     path,
			Layer:    home.LayerUser,
			Severity: home.SeverityWarning,
			Reason:   "read models.json: " + err.Error(),
		}}
	}
	var models map[string]any
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, []home.Diagnostic{{
			File:     path,
			Layer:    home.LayerUser,
			Severity: home.SeverityWarning,
			Reason:   "parse models.json: " + err.Error(),
		}}
	}
	return models, nil
}

func resourceScopeLayer(scope string) home.Layer {
	if scope == ResourceScopeUser {
		return home.LayerUser
	}
	return home.LayerProject
}
