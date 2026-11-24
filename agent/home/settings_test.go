package home

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func diagnosticsWithSeverity(diags []Diagnostic, severity Severity) int {
	n := 0
	for _, d := range diags {
		if d.Severity == severity {
			n++
		}
	}
	return n
}

func TestLoadLayersMerge(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user", "settings.json")
	projectPath := filepath.Join(dir, "project", "settings.json")

	writeTestFile(t, userPath, `{
		"default_provider": "anthropic",
		"default_model": "user-model",
		"default_thinking": "low",
		"tools": {"default": ["read", "+Grep"], "exclude": ["rm"]},
		"compaction": {"enabled": true, "min_messages": 10},
		"resources": {"skills": ["/u/skills"], "prompts": ["/u/prompts"]},
		"packages": [{"name": "a"}]
	}`)
	writeTestFile(t, projectPath, `{
		"default_model": "project-model",
		"tools": {"default": ["-Grep"], "exclude": ["rm", "Bash"]},
		"compaction": {"keep_recent_tokens": 1000},
		"resources": {"skills": ["/p/skills", "/u/skills"], "agents": ["/p/agents"]},
		"packages": [{"name": "a"}, {"name": "b"}]
	}`)

	res, err := LoadLayers(userPath, projectPath, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	s := res.Settings

	if s.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q", s.DefaultProvider)
	}
	if s.DefaultModel != "project-model" {
		t.Errorf("DefaultModel = %q, want project override", s.DefaultModel)
	}
	if s.DefaultThinking != "low" {
		t.Errorf("DefaultThinking = %q, want inherited user value", s.DefaultThinking)
	}
	if res.Sources["default_model"] != LayerProject {
		t.Errorf("default_model source = %v", res.Sources["default_model"])
	}
	if res.Sources["default_thinking"] != LayerUser {
		t.Errorf("default_thinking source = %v", res.Sources["default_thinking"])
	}

	wantDefault := []string{"read", "+Grep", "-Grep"}
	if !reflect.DeepEqual(s.Tools.Default, wantDefault) {
		t.Errorf("Tools.Default = %v, want %v", s.Tools.Default, wantDefault)
	}
	if got := ResolveDefaultTools(s.Tools.Default, []string{"builtin"}); !reflect.DeepEqual(got, []string{"read"}) {
		t.Errorf("ResolveDefaultTools = %v, want [read]", got)
	}
	if !reflect.DeepEqual(s.Tools.Exclude, []string{"rm", "Bash"}) {
		t.Errorf("Tools.Exclude = %v", s.Tools.Exclude)
	}

	if !s.Compaction.Enabled || s.Compaction.MinMessages != 10 || s.Compaction.KeepRecentTokens != 1000 {
		t.Errorf("Compaction = %+v", s.Compaction)
	}
	if res.Sources["compaction.enabled"] != LayerUser || res.Sources["compaction.keep_recent_tokens"] != LayerProject {
		t.Errorf("compaction sources = enabled:%v keep:%v",
			res.Sources["compaction.enabled"], res.Sources["compaction.keep_recent_tokens"])
	}

	wantSkills := []string{"/p/skills", "/u/skills"}
	if !reflect.DeepEqual(s.Resources.Skills, wantSkills) {
		t.Errorf("Resources.Skills = %v, want project-first deduped %v", s.Resources.Skills, wantSkills)
	}
	if !reflect.DeepEqual(s.Resources.Prompts, []string{"/u/prompts"}) {
		t.Errorf("Resources.Prompts = %v", s.Resources.Prompts)
	}
	if !reflect.DeepEqual(s.Resources.Agents, []string{"/p/agents"}) {
		t.Errorf("Resources.Agents = %v", s.Resources.Agents)
	}
	if res.Sources["resources.skills"] != LayerProject || res.Sources["resources.prompts"] != LayerUser {
		t.Errorf("resource sources = %v", res.Sources)
	}

	if len(s.Packages) != 2 || s.Packages[0].Name != "a" || s.Packages[1].Name != "b" {
		t.Errorf("Packages = %+v", s.Packages)
	}
	if diagnosticsWithSeverity(res.Diagnostics, SeverityWarning) == 0 {
		t.Error("expected a duplicate-resource warning")
	}
}

func TestProjectGlobalOnlyKeyRejected(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	projectPath := filepath.Join(dir, "project.json")

	writeTestFile(t, userPath, `{"http_proxy": "http://user-proxy", "default_model": "m"}`)
	writeTestFile(t, projectPath, `{"http_proxy": "http://project-proxy", "self_update_command": "curl x"}`)

	res, err := LoadLayers(userPath, projectPath, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadLayers should not fail without StrictGlobalKeys: %v", err)
	}
	if got := string(res.Settings.Extra["http_proxy"]); got != `"http://user-proxy"` {
		t.Errorf("http_proxy extra = %s, want user value only", got)
	}
	if _, ok := res.Settings.Extra["self_update_command"]; ok {
		t.Error("project self_update_command must not be preserved as a usable setting")
	}
	if diagnosticsWithSeverity(res.Diagnostics, SeverityError) != 2 {
		t.Errorf("diagnostics = %+v, want two errors (http_proxy, self_update_command)", res.Diagnostics)
	}

	if _, err := LoadLayers(userPath, projectPath, LoadOptions{StrictGlobalKeys: true}); err == nil {
		t.Fatal("StrictGlobalKeys should return an error")
	}
}

func TestLoadYAMLAndFindSettingsFile(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "settings.yaml")
	writeTestFile(t, yamlPath, "default_model: yaml-model\nresources:\n  skills:\n    - /y/skills\n")

	res, err := LoadLayers(yamlPath, "", LoadOptions{})
	if err != nil {
		t.Fatalf("LoadLayers yaml: %v", err)
	}
	if res.Settings.DefaultModel != "yaml-model" {
		t.Errorf("DefaultModel = %q", res.Settings.DefaultModel)
	}
	if !reflect.DeepEqual(res.Settings.Resources.Skills, []string{"/y/skills"}) {
		t.Errorf("Skills = %v", res.Settings.Resources.Skills)
	}

	jsonPath := filepath.Join(dir, "settings.json")
	writeTestFile(t, jsonPath, `{"default_model": "json-model"}`)
	found, diag := FindSettingsFile(dir)
	if found != jsonPath {
		t.Fatalf("FindSettingsFile = %q, want json", found)
	}
	if diag == nil || diag.Severity != SeverityWarning {
		t.Fatalf("expected a precedence warning, got %+v", diag)
	}
}

func TestBrokenSettingsProducesDiagnosticNotError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeTestFile(t, path, `{"default_model": `)

	res, err := LoadLayers(path, "", LoadOptions{})
	if err != nil {
		t.Fatalf("broken file must not fail loading: %v", err)
	}
	if diagnosticsWithSeverity(res.Diagnostics, SeverityError) == 0 {
		t.Fatal("expected an error diagnostic")
	}
	if res.Settings.DefaultModel != "" {
		t.Errorf("partial settings loaded: %+v", res.Settings)
	}
}

func TestUnknownKeysPreservedAndMarshaled(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "settings.json")
	writeTestFile(t, userPath, `{
		"default_model": "m",
		"plugins_extra": {"a": 1},
		"tools": {"custom": true}
	}`)

	res, err := LoadLayers(userPath, "", LoadOptions{})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if _, ok := res.Settings.Extra["plugins_extra"]; !ok {
		t.Fatal("unknown top-level key not preserved")
	}
	if _, ok := res.Settings.Extra["tools.custom"]; !ok {
		t.Fatal("unknown nested key not preserved")
	}

	data, err := json.Marshal(res.Settings)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["plugins_extra"]; !ok {
		t.Errorf("plugins_extra lost on marshal: %s", data)
	}
	var tools map[string]json.RawMessage
	if err := json.Unmarshal(out["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools["custom"]; !ok {
		t.Errorf("tools.custom lost on marshal: %s", data)
	}
}

func TestLoadPathsUsesProjectLayer(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings.json"), `{"default_model": "user"}`)
	writeTestFile(t, filepath.Join(cwd, ".cortex", "settings.json"), `{"default_model": "project"}`)

	p, err := Resolve(Options{Home: root, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	res, err := LoadPaths(p, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadPaths: %v", err)
	}
	if res.Settings.DefaultModel != "project" {
		t.Fatalf("DefaultModel = %q", res.Settings.DefaultModel)
	}

	res, err = LoadPaths(p, LoadOptions{ProjectUntrusted: true})
	if err != nil {
		t.Fatalf("LoadPaths: %v", err)
	}
	if res.Settings.DefaultModel != "user" {
		t.Fatalf("untrusted project still applied: %q", res.Settings.DefaultModel)
	}
}

func TestResolveDefaultTools(t *testing.T) {
	tests := []struct {
		entries  []string
		fallback []string
		want     []string
	}{
		{[]string{"a", "+b", "-a"}, []string{"x"}, []string{"b"}},
		{[]string{"+c"}, []string{"a", "b"}, []string{"a", "b", "c"}},
		{[]string{"-a"}, []string{"a", "b"}, []string{"b"}},
		{[]string{"only"}, []string{"a"}, []string{"only"}},
		{[]string{}, []string{"a"}, []string{}},
	}
	for _, tt := range tests {
		if got := ResolveDefaultTools(tt.entries, tt.fallback); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ResolveDefaultTools(%v, %v) = %v, want %v", tt.entries, tt.fallback, got, tt.want)
		}
	}
}

func TestPackageIdentity(t *testing.T) {
	if got := (PackageRef{Name: "n", Source: "s"}).Identity(); got != "name:n" {
		t.Errorf("Identity = %q", got)
	}
	if got := (PackageRef{Source: "s"}).Identity(); got != "source:s" {
		t.Errorf("Identity = %q", got)
	}
}
