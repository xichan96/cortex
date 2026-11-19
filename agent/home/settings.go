package home

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Layer identifies where a setting value came from.
type Layer int

const (
	LayerBuiltin Layer = iota
	LayerUser
	LayerProject
	LayerEnv
	LayerCLI
)

func (l Layer) String() string {
	switch l {
	case LayerBuiltin:
		return "builtin"
	case LayerUser:
		return "user"
	case LayerProject:
		return "project"
	case LayerEnv:
		return "env"
	case LayerCLI:
		return "cli"
	default:
		return fmt.Sprintf("layer(%d)", int(l))
	}
}

// Severity classifies a load diagnostic.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	default:
		return fmt.Sprintf("severity(%d)", int(s))
	}
}

// Diagnostic describes a non-fatal problem found while loading a settings
// layer. A broken file never aborts loading; it is reported here instead.
type Diagnostic struct {
	File     string
	Key      string
	Reason   string
	Layer    Layer
	Severity Severity
}

func (d Diagnostic) Error() string {
	location := d.File
	if location == "" {
		location = d.Layer.String()
	}
	if d.Key != "" {
		return fmt.Sprintf("%s: %s: %s: %s", d.Severity, location, d.Key, d.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", d.Severity, location, d.Reason)
}

// Settings is the merged agent settings structure. Unknown keys are preserved
// in Extra so plugins can read them and a later write-back keeps them intact.
type Settings struct {
	DefaultProvider string             `json:"default_provider,omitempty"`
	DefaultModel    string             `json:"default_model,omitempty"`
	DefaultThinking string             `json:"default_thinking,omitempty"`
	Tools           ToolSettings       `json:"tools,omitempty"`
	Compaction      CompactionSettings `json:"compaction,omitempty"`
	Resources       ResourceSettings   `json:"resources,omitempty"`
	Packages        []PackageRef       `json:"packages,omitempty"`

	// Extra holds every key this package does not understand, both top-level
	// keys ("foo") and nested ones ("tools.foo"), as raw JSON.
	Extra map[string]json.RawMessage `json:"-"`
}

// ToolSettings controls the default tool selection.
type ToolSettings struct {
	// Default entries are either plain tool names or "+name"/"-name"
	// modifiers. Plain names replace the inherited selection; modifiers adjust
	// it in order.
	Default []string `json:"default,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// CompactionSettings mirrors the compaction knobs used by the engine. Values
// are zero when unset; merge decisions use per-key presence instead.
type CompactionSettings struct {
	Enabled           bool   `json:"enabled,omitempty"`
	MinMessages       int    `json:"min_messages,omitempty"`
	KeepRecentTokens  int    `json:"keep_recent_tokens,omitempty"`
	CacheAnchorTokens int    `json:"cache_anchor_tokens,omitempty"`
	Model             string `json:"model,omitempty"`
}

// ResourceSettings lists resource roots, highest priority first after merging.
type ResourceSettings struct {
	Skills  []string `json:"skills,omitempty"`
	Prompts []string `json:"prompts,omitempty"`
	Agents  []string `json:"agents,omitempty"`
	Plugins []string `json:"plugins,omitempty"`
}

// PackageRef declares a package to install or load. Identity is Name, then
// Source, then Version.
type PackageRef struct {
	Name    string `json:"name,omitempty"`
	Source  string `json:"source,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Version string `json:"version,omitempty"`
}

// Identity returns the dedupe key for a package reference.
func (p PackageRef) Identity() string {
	switch {
	case p.Name != "":
		return "name:" + p.Name
	case p.Source != "":
		return "source:" + p.Source
	default:
		return "version:" + p.Version
	}
}

// GlobalOnlySettingKeys may only appear in the user layer. A project setting
// one of these would let a repository override the trust model, so it is
// rejected.
var GlobalOnlySettingKeys = []string{
	"default_project_trust",
	"http_proxy",
	"self_update_command",
}

// LoadOptions controls settings loading.
type LoadOptions struct {
	// StrictGlobalKeys turns a project-layer global-only key into a returned
	// error instead of a recorded SeverityError diagnostic.
	StrictGlobalKeys bool
	// ProjectUntrusted skips the project layer entirely (resources from an
	// untrusted project must not be loaded).
	ProjectUntrusted bool
}

// Resolved is the merged settings plus per-key provenance and diagnostics.
type Resolved struct {
	Settings    Settings
	Sources     map[string]Layer
	Diagnostics []Diagnostic
}

// LoadLayers loads and merges user then project settings. Scalars are
// overridden by the project layer; resource lists are merged with project
// entries first and deduplicated; unknown keys are preserved.
//
// A missing file is not an error. A malformed file produces diagnostics and is
// skipped. The only returned error is a strict global-only-key violation in the
// project layer.
func LoadLayers(userPath, projectPath string, opts LoadOptions) (*Resolved, error) {
	res := &Resolved{Sources: map[string]Layer{}}

	userSettings, userPresent, userDiags, err := loadSettingsLayer(userPath, LayerUser, false, opts.StrictGlobalKeys)
	if err != nil {
		return nil, err
	}
	res.Diagnostics = append(res.Diagnostics, userDiags...)

	var projectSettings Settings
	projectPresent := map[string]bool{}
	if projectPath != "" && !opts.ProjectUntrusted {
		var projectDiags []Diagnostic
		projectSettings, projectPresent, projectDiags, err = loadSettingsLayer(projectPath, LayerProject, true, opts.StrictGlobalKeys)
		if err != nil {
			return nil, err
		}
		res.Diagnostics = append(res.Diagnostics, projectDiags...)
	} else if opts.ProjectUntrusted && projectPath != "" {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			File:     projectPath,
			Layer:    LayerProject,
			Severity: SeverityInfo,
			Reason:   "project layer skipped: project is not trusted",
		})
	}

	merged, sources, mergeDiags := mergeLayers(userSettings, userPresent, projectSettings, projectPresent, projectPath)
	res.Settings = merged
	res.Diagnostics = append(res.Diagnostics, mergeDiags...)
	for k, v := range sources {
		res.Sources[k] = v
	}
	return res, nil
}

// LoadPaths resolves the conventional settings files under an agent home and
// workspace, then loads them with LoadLayers.
func LoadPaths(p Paths, opts LoadOptions) (*Resolved, error) {
	userPath, userDiag := FindSettingsFile(p.Root)
	projectPath, projectDiag := FindSettingsFile(p.ProjectDir)

	res, err := LoadLayers(userPath, projectPath, opts)
	if res == nil {
		res = &Resolved{Sources: map[string]Layer{}}
	}
	if userDiag != nil {
		userDiag.Layer = LayerUser
		res.Diagnostics = append(res.Diagnostics, *userDiag)
	}
	if projectDiag != nil {
		projectDiag.Layer = LayerProject
		res.Diagnostics = append(res.Diagnostics, *projectDiag)
	}
	return res, err
}

// FindSettingsFile returns the settings file for dir. settings.json wins over
// settings.yaml/settings.yml in the same directory and yields a warning. When
// no file exists, <dir>/settings.json is returned as the default write target.
func FindSettingsFile(dir string) (string, *Diagnostic) {
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	jsonPath := filepath.Join(dir, "settings.json")
	jsonExists := isRegularFile(jsonPath)

	var yamlPath string
	for _, name := range []string{"settings.yaml", "settings.yml"} {
		candidate := filepath.Join(dir, name)
		if isRegularFile(candidate) {
			yamlPath = candidate
			break
		}
	}

	if jsonExists {
		if yamlPath != "" {
			return jsonPath, &Diagnostic{
				File:     jsonPath,
				Layer:    LayerUser,
				Severity: SeverityWarning,
				Reason:   fmt.Sprintf("both settings.json and %s exist; settings.json takes precedence", filepath.Base(yamlPath)),
			}
		}
		return jsonPath, nil
	}
	if yamlPath != "" {
		return yamlPath, nil
	}
	return jsonPath, nil
}

func loadSettingsLayer(path string, layer Layer, project, strictGlobalKeys bool) (Settings, map[string]bool, []Diagnostic, error) {
	settings := Settings{}
	present := map[string]bool{}
	if strings.TrimSpace(path) == "" {
		return settings, present, nil, nil
	}
	path = resolveSettingsFilePath(path)

	raw, diags, err := readSettingsFile(path, layer)
	if err != nil {
		return settings, present, diags, err
	}
	if raw == nil {
		return settings, present, diags, nil
	}

	globalOnly := make(map[string]bool, len(GlobalOnlySettingKeys))
	for _, key := range GlobalOnlySettingKeys {
		globalOnly[key] = true
	}

	extra := map[string]json.RawMessage{}
	for key, value := range raw {
		if globalOnly[key] {
			if project {
				diags = append(diags, Diagnostic{
					File:     path,
					Key:      key,
					Layer:    layer,
					Severity: SeverityError,
					Reason:   "global-only setting may not be set in project settings",
				})
				continue
			}
			extra[key] = value
			continue
		}

		switch key {
		case "default_provider":
			if v, ok := decodeString(value, path, key, layer, &diags); ok {
				settings.DefaultProvider = v
				present[key] = true
			}
		case "default_model":
			if v, ok := decodeString(value, path, key, layer, &diags); ok {
				settings.DefaultModel = v
				present[key] = true
			}
		case "default_thinking":
			if v, ok := decodeString(value, path, key, layer, &diags); ok {
				settings.DefaultThinking = v
				present[key] = true
			}
		case "tools":
			decodeTools(value, path, layer, &settings.Tools, present, extra, &diags)
		case "compaction":
			decodeCompaction(value, path, layer, &settings.Compaction, present, extra, &diags)
		case "resources":
			decodeResources(value, path, layer, &settings.Resources, present, extra, &diags)
		case "packages":
			if v, ok := decodePackages(value, path, layer, &diags); ok {
				settings.Packages = v
				present[key] = true
			}
		default:
			extra[key] = value
			diags = append(diags, Diagnostic{
				File:     path,
				Key:      key,
				Layer:    layer,
				Severity: SeverityInfo,
				Reason:   "unknown key preserved",
			})
		}
	}

	// A strict project layer rejects global-only keys. Do this after parsing so
	// callers get every diagnostic, then fail.
	if project && strictGlobalKeys {
		for _, d := range diags {
			if d.Severity == SeverityError && globalOnly[d.Key] {
				return settings, present, diags, fmt.Errorf("home: %s: %s", path, d.Reason)
			}
		}
	}

	if len(extra) > 0 {
		settings.Extra = extra
	}
	return settings, present, diags, nil
}

func resolveSettingsFilePath(path string) string {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		resolved, _ := FindSettingsFile(path)
		return resolved
	}
	return path
}

func readSettingsFile(path string, layer Layer) (map[string]json.RawMessage, []Diagnostic, error) {
	var diags []Diagnostic

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, []Diagnostic{{
			File: path, Layer: layer, Severity: SeverityError,
			Reason: fmt.Sprintf("read failed: %v", err),
		}}, nil
	}
	data = stripBOM(data)
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, diags, nil
	}

	var raw map[string]json.RawMessage
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		var decoded any
		if err := yaml.Unmarshal(data, &decoded); err != nil {
			return nil, []Diagnostic{{
				File: path, Layer: layer, Severity: SeverityError,
				Reason: fmt.Sprintf("invalid YAML: %v", err),
			}}, nil
		}
		converted, err := yamlValueToRawMap(decoded)
		if err != nil {
			return nil, []Diagnostic{{
				File: path, Layer: layer, Severity: SeverityError, Reason: err.Error(),
			}}, nil
		}
		raw = converted
	default:
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, []Diagnostic{{
				File: path, Layer: layer, Severity: SeverityError,
				Reason: fmt.Sprintf("invalid JSON: %v", err),
			}}, nil
		}
	}
	if raw == nil {
		return nil, []Diagnostic{{
			File: path, Layer: layer, Severity: SeverityError,
			Reason: "settings must be a JSON/YAML object",
		}}, nil
	}
	return raw, diags, nil
}

func yamlValueToRawMap(decoded any) (map[string]json.RawMessage, error) {
	switch v := decoded.(type) {
	case nil:
		return map[string]json.RawMessage{}, nil
	case map[string]any:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("convert YAML settings: %w", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &raw); err != nil {
			return nil, fmt.Errorf("convert YAML settings: %w", err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("settings must be a JSON/YAML object")
	}
}

func decodeString(raw json.RawMessage, file, key string, layer Layer, diags *[]Diagnostic) (string, bool) {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: key, Layer: layer, Severity: SeverityError,
			Reason: "expected a string",
		})
		return "", false
	}
	return v, true
}

func decodeStringSlice(raw json.RawMessage, file, key string, layer Layer, diags *[]Diagnostic) ([]string, bool) {
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: key, Layer: layer, Severity: SeverityError,
			Reason: "expected an array of strings",
		})
		return nil, false
	}
	return v, true
}

func decodeInt(raw json.RawMessage, file, key string, layer Layer, diags *[]Diagnostic) (int, bool) {
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: key, Layer: layer, Severity: SeverityError,
			Reason: "expected an integer",
		})
		return 0, false
	}
	return v, true
}

func decodeBool(raw json.RawMessage, file, key string, layer Layer, diags *[]Diagnostic) (bool, bool) {
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: key, Layer: layer, Severity: SeverityError,
			Reason: "expected a boolean",
		})
		return false, false
	}
	return v, true
}

func decodeObject(raw json.RawMessage, file, key string, layer Layer, diags *[]Diagnostic) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: key, Layer: layer, Severity: SeverityError,
			Reason: "expected an object",
		})
		return nil, false
	}
	return obj, true
}

func decodeTools(raw json.RawMessage, file string, layer Layer, dst *ToolSettings, present map[string]bool, extra map[string]json.RawMessage, diags *[]Diagnostic) {
	obj, ok := decodeObject(raw, file, "tools", layer, diags)
	if !ok {
		return
	}
	for key, value := range obj {
		fullKey := "tools." + key
		switch key {
		case "default":
			if v, ok := decodeStringSlice(value, file, fullKey, layer, diags); ok {
				dst.Default = v
				present[fullKey] = true
			}
		case "exclude":
			if v, ok := decodeStringSlice(value, file, fullKey, layer, diags); ok {
				dst.Exclude = v
				present[fullKey] = true
			}
		default:
			extra[fullKey] = value
			*diags = append(*diags, Diagnostic{
				File: file, Key: fullKey, Layer: layer, Severity: SeverityInfo,
				Reason: "unknown key preserved",
			})
		}
	}
}

func decodeCompaction(raw json.RawMessage, file string, layer Layer, dst *CompactionSettings, present map[string]bool, extra map[string]json.RawMessage, diags *[]Diagnostic) {
	obj, ok := decodeObject(raw, file, "compaction", layer, diags)
	if !ok {
		return
	}
	for key, value := range obj {
		fullKey := "compaction." + key
		switch key {
		case "enabled":
			if v, ok := decodeBool(value, file, fullKey, layer, diags); ok {
				dst.Enabled = v
				present[fullKey] = true
			}
		case "min_messages":
			if v, ok := decodeInt(value, file, fullKey, layer, diags); ok {
				dst.MinMessages = v
				present[fullKey] = true
			}
		case "keep_recent_tokens":
			if v, ok := decodeInt(value, file, fullKey, layer, diags); ok {
				dst.KeepRecentTokens = v
				present[fullKey] = true
			}
		case "cache_anchor_tokens":
			if v, ok := decodeInt(value, file, fullKey, layer, diags); ok {
				dst.CacheAnchorTokens = v
				present[fullKey] = true
			}
		case "model":
			if v, ok := decodeString(value, file, fullKey, layer, diags); ok {
				dst.Model = v
				present[fullKey] = true
			}
		default:
			extra[fullKey] = value
			*diags = append(*diags, Diagnostic{
				File: file, Key: fullKey, Layer: layer, Severity: SeverityInfo,
				Reason: "unknown key preserved",
			})
		}
	}
}

func decodeResources(raw json.RawMessage, file string, layer Layer, dst *ResourceSettings, present map[string]bool, extra map[string]json.RawMessage, diags *[]Diagnostic) {
	obj, ok := decodeObject(raw, file, "resources", layer, diags)
	if !ok {
		return
	}
	for key, value := range obj {
		fullKey := "resources." + key
		switch key {
		case "skills", "prompts", "agents", "plugins":
			v, ok := decodeStringSlice(value, file, fullKey, layer, diags)
			if !ok {
				continue
			}
			present[fullKey] = true
			switch key {
			case "skills":
				dst.Skills = v
			case "prompts":
				dst.Prompts = v
			case "agents":
				dst.Agents = v
			case "plugins":
				dst.Plugins = v
			}
		default:
			extra[fullKey] = value
			*diags = append(*diags, Diagnostic{
				File: file, Key: fullKey, Layer: layer, Severity: SeverityInfo,
				Reason: "unknown key preserved",
			})
		}
	}
}

func decodePackages(raw json.RawMessage, file string, layer Layer, diags *[]Diagnostic) ([]PackageRef, bool) {
	var refs []PackageRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		*diags = append(*diags, Diagnostic{
			File: file, Key: "packages", Layer: layer, Severity: SeverityError,
			Reason: "expected an array of package references",
		})
		return nil, false
	}
	return refs, true
}

func mergeLayers(user Settings, userPresent map[string]bool, project Settings, projectPresent map[string]bool, projectPath string) (Settings, map[string]Layer, []Diagnostic) {
	var diags []Diagnostic
	sources := map[string]Layer{}

	setScalar := func(key string, userValue, projectValue string) string {
		switch {
		case projectPresent[key]:
			sources[key] = LayerProject
			return projectValue
		case userPresent[key]:
			sources[key] = LayerUser
			return userValue
		default:
			return ""
		}
	}

	merged := Settings{
		DefaultProvider: setScalar("default_provider", user.DefaultProvider, project.DefaultProvider),
		DefaultModel:    setScalar("default_model", user.DefaultModel, project.DefaultModel),
		DefaultThinking: setScalar("default_thinking", user.DefaultThinking, project.DefaultThinking),
	}

	// Tool defaults use modifier semantics: a project list of only +/- entries
	// adjusts the inherited selection; plain names replace it.
	merged.Tools.Default = mergeToolDefaults(user.Tools.Default, project.Tools.Default, projectPresent["tools.default"])
	if projectPresent["tools.default"] {
		sources["tools.default"] = LayerProject
	} else if userPresent["tools.default"] {
		sources["tools.default"] = LayerUser
	}
	merged.Tools.Exclude = dedupeStrings(append(append([]string{}, user.Tools.Exclude...), project.Tools.Exclude...))
	if projectPresent["tools.exclude"] {
		sources["tools.exclude"] = LayerProject
	} else if userPresent["tools.exclude"] {
		sources["tools.exclude"] = LayerUser
	}

	merged.Compaction = mergeCompaction(user.Compaction, userPresent, project.Compaction, projectPresent, sources)

	merged.Resources = ResourceSettings{
		Skills:  mergeResourceLists(user.Resources.Skills, project.Resources.Skills, "resources.skills", projectPath, &sources, &diags),
		Prompts: mergeResourceLists(user.Resources.Prompts, project.Resources.Prompts, "resources.prompts", projectPath, &sources, &diags),
		Agents:  mergeResourceLists(user.Resources.Agents, project.Resources.Agents, "resources.agents", projectPath, &sources, &diags),
		Plugins: mergeResourceLists(user.Resources.Plugins, project.Resources.Plugins, "resources.plugins", projectPath, &sources, &diags),
	}

	merged.Packages = mergePackages(user.Packages, project.Packages, &sources, &diags)

	merged.Extra = mergeExtra(user.Extra, project.Extra, sources)
	return merged, sources, diags
}

func mergeCompaction(user CompactionSettings, userPresent map[string]bool, project CompactionSettings, projectPresent map[string]bool, sources map[string]Layer) CompactionSettings {
	pickInt := func(key string, userValue, projectValue int) int {
		if projectPresent[key] {
			sources[key] = LayerProject
			return projectValue
		}
		if userPresent[key] {
			sources[key] = LayerUser
		}
		return userValue
	}
	pickString := func(key string, userValue, projectValue string) string {
		if projectPresent[key] {
			sources[key] = LayerProject
			return projectValue
		}
		if userPresent[key] {
			sources[key] = LayerUser
		}
		return userValue
	}
	pickBool := func(key string, userValue, projectValue bool) bool {
		if projectPresent[key] {
			sources[key] = LayerProject
			return projectValue
		}
		if userPresent[key] {
			sources[key] = LayerUser
		}
		return userValue
	}

	return CompactionSettings{
		Enabled:           pickBool("compaction.enabled", user.Enabled, project.Enabled),
		MinMessages:       pickInt("compaction.min_messages", user.MinMessages, project.MinMessages),
		KeepRecentTokens:  pickInt("compaction.keep_recent_tokens", user.KeepRecentTokens, project.KeepRecentTokens),
		CacheAnchorTokens: pickInt("compaction.cache_anchor_tokens", user.CacheAnchorTokens, project.CacheAnchorTokens),
		Model:             pickString("compaction.model", user.Model, project.Model),
	}
}

func mergeToolDefaults(user, project []string, projectOverrides bool) []string {
	if !projectOverrides {
		return append([]string{}, user...)
	}
	if len(user) == 0 || allToolModifiers(project) {
		if len(project) == 0 {
			return []string{}
		}
		return append(append([]string{}, user...), project...)
	}
	return append([]string{}, project...)
}

func allToolModifiers(entries []string) bool {
	for _, entry := range entries {
		if !isToolModifier(entry) {
			return false
		}
	}
	return len(entries) > 0
}

func isToolModifier(entry string) bool {
	e := strings.TrimSpace(entry)
	return len(e) > 1 && (e[0] == '+' || e[0] == '-')
}

// ResolveDefaultTools expands a modifier list into a concrete tool selection.
// Plain entries replace fallback; "+name" adds and "-name" removes a tool.
func ResolveDefaultTools(entries, fallback []string) []string {
	var selected []string
	hasPlain := false
	for _, entry := range entries {
		if !isToolModifier(entry) && strings.TrimSpace(entry) != "" {
			hasPlain = true
			break
		}
	}
	if hasPlain || len(entries) == 0 {
		for _, entry := range entries {
			e := strings.TrimSpace(entry)
			if e == "" || isToolModifier(e) {
				continue
			}
			selected = append(selected, e)
		}
	} else {
		selected = append(selected, fallback...)
	}
	for _, entry := range entries {
		e := strings.TrimSpace(entry)
		if len(e) < 2 || (e[0] != '+' && e[0] != '-') {
			continue
		}
		name := strings.TrimSpace(e[1:])
		if name == "" {
			continue
		}
		switch e[0] {
		case '+':
			selected = appendUnique(selected, name)
		case '-':
			selected = removeString(selected, name)
		}
	}
	return dedupeStrings(selected)
}

func mergeResourceLists(user, project []string, key, projectPath string, sources *map[string]Layer, diags *[]Diagnostic) []string {
	// Project entries come first: the merged list doubles as discovery order,
	// and the project layer has higher priority.
	combined := make([]string, 0, len(user)+len(project))
	seen := map[string]bool{}
	for _, entry := range project {
		e := strings.TrimSpace(entry)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		combined = append(combined, e)
	}
	for _, entry := range user {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		if seen[e] {
			*diags = append(*diags, Diagnostic{
				File:     projectPath,
				Key:      key,
				Layer:    LayerProject,
				Severity: SeverityWarning,
				Reason:   fmt.Sprintf("duplicate resource %q in project layer; the higher-priority project entry wins", e),
			})
			continue
		}
		seen[e] = true
		combined = append(combined, e)
	}
	if len(project) > 0 {
		(*sources)[key] = LayerProject
	} else if len(user) > 0 {
		(*sources)[key] = LayerUser
	}
	return combined
}

func mergePackages(user, project []PackageRef, sources *map[string]Layer, diags *[]Diagnostic) []PackageRef {
	combined := make([]PackageRef, 0, len(user)+len(project))
	seen := map[string]bool{}
	for _, ref := range project {
		id := ref.Identity()
		if seen[id] {
			continue
		}
		seen[id] = true
		combined = append(combined, ref)
	}
	for _, ref := range user {
		id := ref.Identity()
		if seen[id] {
			*diags = append(*diags, Diagnostic{
				Key:      "packages",
				Layer:    LayerProject,
				Severity: SeverityWarning,
				Reason:   fmt.Sprintf("duplicate package %q; the higher-priority project entry wins", id),
			})
			continue
		}
		seen[id] = true
		combined = append(combined, ref)
	}
	if len(project) > 0 {
		(*sources)["packages"] = LayerProject
	} else if len(user) > 0 {
		(*sources)["packages"] = LayerUser
	}
	return combined
}

func mergeExtra(user, project map[string]json.RawMessage, sources map[string]Layer) map[string]json.RawMessage {
	merged := map[string]json.RawMessage{}
	for k, v := range user {
		merged[k] = v
		sources[k] = LayerUser
	}
	for k, v := range project {
		merged[k] = v
		sources[k] = LayerProject
	}
	return merged
}

func dedupeStrings(entries []string) []string {
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		e := strings.TrimSpace(entry)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

func removeString(list []string, value string) []string {
	out := list[:0]
	for _, v := range list {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func stripBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

type settingsAlias Settings

// MarshalJSON serializes known fields plus Extra. Dotted Extra keys
// ("tools.foo") are merged back into their nested object so unknown keys
// survive a read/write round trip. Known fields always win on collision.
func (s Settings) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(settingsAlias(s))
	if err != nil {
		return nil, err
	}
	if len(s.Extra) == 0 {
		return base, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	for key, value := range s.Extra {
		section, nested, ok := strings.Cut(key, ".")
		if !ok {
			if _, exists := obj[key]; !exists {
				obj[key] = value
			}
			continue
		}

		sectionObj := map[string]json.RawMessage{}
		if existing, exists := obj[section]; exists && len(existing) > 0 {
			if err := json.Unmarshal(existing, &sectionObj); err != nil {
				// The known field is not an object; leave it untouched.
				continue
			}
		}
		if _, exists := sectionObj[nested]; exists {
			continue
		}
		sectionObj[nested] = value
		encoded, err := json.Marshal(sectionObj)
		if err != nil {
			return nil, err
		}
		obj[section] = encoded
	}
	return json.Marshal(obj)
}
