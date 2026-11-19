package skills

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// agentskills.io specification limits.
const (
	MaxNameLength        = 64
	MaxDescriptionLength = 1024
)

// Severity classifies a Diagnostic.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic kinds emitted by parsing and discovery.
const (
	DiagnosticDeprecated      = "deprecated"
	DiagnosticUnknownField    = "unknown-field"
	DiagnosticInvalidName     = "invalid-name"
	DiagnosticInvalidDesc     = "invalid-description"
	DiagnosticMissingDesc     = "missing-description"
	DiagnosticInvalidField    = "invalid-field"
	DiagnosticInvalidMetadata = "invalid-metadata"
	DiagnosticCollision       = "collision"
	DiagnosticReadError       = "read-error"
	DiagnosticParseError      = "parse-error"
	DiagnosticUnsupportedRoot = "unsupported-root"
	DiagnosticUnreadableDir   = "unreadable-directory"
)

// Diagnostic is a non-fatal problem found while parsing or discovering skills.
// Diagnostics never prevent startup by themselves; the caller decides whether a
// skill with diagnostics is still usable (for example a missing description
// causes discovery to skip the file, while an invalid name only warns).
type Diagnostic struct {
	Kind     string
	Severity Severity
	Path     string
	Message  string
}

// String renders the diagnostic as "path: message".
func (d Diagnostic) String() string {
	if d.Path == "" {
		return d.Message
	}
	return fmt.Sprintf("%s: %s", d.Path, d.Message)
}

// CortexMetadata holds cortex-specific skill extensions. The portable
// agentskills.io core lives directly on Spec; everything that is not part of
// the portable spec lives here, normally declared under metadata.cortex.
type CortexMetadata struct {
	Triggers     []Trigger
	Tags         []string
	Priority     int
	Dependencies []string
	Paths        []string
	WhenToUse    string
	Timeout      time.Duration
	Retries      int
	Fallback     string
}

// Spec is the parsed metadata of a SKILL.md (or standalone .md) file.
type Spec struct {
	// Portable agentskills.io fields.
	Name                   string
	Description            string
	License                string
	Compatibility          string
	Metadata               map[string]interface{}
	AllowedTools           []string
	DisableModelInvocation bool

	// Cortex extensions (metadata.cortex, with legacy top-level fallback).
	Cortex CortexMetadata

	// Source information.
	Path    string // absolute path to the skill file
	BaseDir string // directory used to resolve relative references
}

// Document is a parsed skill file: metadata plus the body with frontmatter
// stripped.
type Document struct {
	Spec *Spec
	Body string
}

// legacyExtensionKeys are cortex fields historically declared at the top
// level. They are still parsed but emit a deprecation diagnostic.
var legacyExtensionKeys = []string{
	"triggers", "tags", "priority", "dependencies", "paths",
	"when_to_use", "timeout", "retries", "fallback",
}

var knownTopLevelFields = map[string]struct{}{
	"name": {}, "description": {}, "license": {}, "compatibility": {},
	"metadata": {}, "allowed-tools": {}, "disable-model-invocation": {},
	// Legacy spellings.
	"allowed_tools": {},
	"triggers":      {}, "tags": {}, "priority": {}, "dependencies": {},
	"paths": {}, "when_to_use": {}, "timeout": {}, "retries": {}, "fallback": {},
}

// ParseSpec parses a skill file and returns only its metadata.
func ParseSpec(data []byte, sourcePath string) (*Spec, []Diagnostic, error) {
	doc, diags, err := ParseDocument(data, sourcePath)
	if err != nil {
		return nil, diags, err
	}
	return doc.Spec, diags, nil
}

// ParseDocument parses a skill file into metadata and body. The frontmatter is
// optional; a file without frontmatter is still parseable (name falls back to
// the file/directory name) but will carry a missing-description diagnostic.
func ParseDocument(data []byte, sourcePath string) (*Document, []Diagnostic, error) {
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", sourcePath, err)
	}

	var raw map[string]interface{}
	if strings.TrimSpace(frontmatter) != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
			return nil, nil, fmt.Errorf("%s: parse frontmatter: %w", sourcePath, err)
		}
	}
	if raw == nil {
		raw = map[string]interface{}{}
	}

	spec := &Spec{
		Path:    filepath.Clean(sourcePath),
		BaseDir: filepath.Dir(filepath.Clean(sourcePath)),
	}

	var diags []Diagnostic
	addDiag := func(kind, format string, args ...interface{}) {
		diags = append(diags, Diagnostic{
			Kind:     kind,
			Severity: SeverityWarning,
			Path:     spec.Path,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	// Portable fields.
	if v, ok := raw["name"]; ok {
		if s, ok := asString(v); ok {
			spec.Name = strings.TrimSpace(s)
		} else {
			addDiag(DiagnosticInvalidField, "field %q must be a string", "name")
		}
	}
	if spec.Name == "" {
		spec.Name = defaultSkillName(spec.Path)
	}

	if v, ok := raw["description"]; ok {
		if s, ok := asString(v); ok {
			spec.Description = strings.TrimSpace(s)
		} else {
			addDiag(DiagnosticInvalidField, "field %q must be a string", "description")
		}
	}
	if v, ok := raw["license"]; ok {
		if s, ok := asString(v); ok {
			spec.License = s
		}
	}
	if v, ok := raw["compatibility"]; ok {
		if s, ok := asString(v); ok {
			spec.Compatibility = s
		}
	}
	if v, ok := raw["disable-model-invocation"]; ok {
		if b, ok := asBool(v); ok {
			spec.DisableModelInvocation = b
		} else {
			addDiag(DiagnosticInvalidField, "field %q must be a boolean", "disable-model-invocation")
		}
	}

	// allowed-tools is the portable spelling; allowed_tools is the legacy one.
	if v, ok := raw["allowed-tools"]; ok {
		spec.AllowedTools = asStringSlice(v)
	}
	if v, ok := raw["allowed_tools"]; ok {
		addDiag(DiagnosticDeprecated, "field %q is deprecated; use %q", "allowed_tools", "allowed-tools")
		if _, hasNew := raw["allowed-tools"]; !hasNew {
			spec.AllowedTools = asStringSlice(v)
		}
	}

	// metadata keeps the raw map (other vendors' namespaces included); the
	// cortex namespace is parsed into Spec.Cortex.
	var cortexMap map[string]interface{}
	if v, ok := raw["metadata"]; ok {
		if m, ok := asMap(v); ok {
			spec.Metadata = m
			if cv, ok := m["cortex"]; ok {
				if cm, ok := asMap(cv); ok {
					cortexMap = cm
				} else {
					addDiag(DiagnosticInvalidMetadata, "metadata.cortex must be a mapping")
				}
			}
		} else {
			addDiag(DiagnosticInvalidMetadata, "field %q must be a mapping", "metadata")
		}
	}

	// Legacy top-level extension fields: parse them, but let metadata.cortex win.
	for _, key := range legacyExtensionKeys {
		v, ok := raw[key]
		if !ok {
			continue
		}
		addDiag(DiagnosticDeprecated, "field %q is deprecated; move it under metadata.cortex", key)
		if _, hasNew := cortexMap[key]; hasNew {
			continue
		}
		if msg := applyCortexField(&spec.Cortex, key, v); msg != "" {
			addDiag(DiagnosticInvalidField, "%s", msg)
		}
	}
	for key, v := range cortexMap {
		if !isCortexExtensionKey(key) {
			addDiag(DiagnosticUnknownField, "unknown metadata.cortex field %q", key)
			continue
		}
		if msg := applyCortexField(&spec.Cortex, key, v); msg != "" {
			addDiag(DiagnosticInvalidField, "%s", msg)
		}
	}

	// Unknown top-level fields are reported (design 6.6), but never fatal.
	for key := range raw {
		if _, ok := knownTopLevelFields[key]; !ok {
			addDiag(DiagnosticUnknownField, "unknown frontmatter field %q", key)
		}
	}

	for _, msg := range ValidateSkillName(spec.Name) {
		addDiag(DiagnosticInvalidName, "invalid skill name %q: %s", spec.Name, msg)
	}
	for _, msg := range ValidateSkillDescription(spec.Description) {
		kind := DiagnosticInvalidDesc
		if strings.TrimSpace(spec.Description) == "" {
			kind = DiagnosticMissingDesc
		}
		addDiag(kind, "invalid skill description: %s", msg)
	}

	return &Document{Spec: spec, Body: body}, diags, nil
}

// ValidateSkillName checks a name against the agentskills.io rules and returns
// human-readable problems. An empty slice means the name is valid.
func ValidateSkillName(name string) []string {
	var problems []string
	if name == "" {
		return []string{"name is required"}
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		problems = append(problems, fmt.Sprintf("exceeds %d characters", MaxNameLength))
	}
	validChars := true
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			validChars = false
			break
		}
	}
	if !validChars {
		problems = append(problems, "must contain only lowercase letters, digits, and hyphens")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		problems = append(problems, "must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		problems = append(problems, "must not contain consecutive hyphens")
	}
	return problems
}

// ValidateSkillDescription checks a description against the agentskills.io
// rules and returns human-readable problems.
func ValidateSkillDescription(desc string) []string {
	trimmed := strings.TrimSpace(desc)
	if trimmed == "" {
		return []string{"description is required"}
	}
	if utf8.RuneCountInString(trimmed) > MaxDescriptionLength {
		return []string{fmt.Sprintf("exceeds %d characters", MaxDescriptionLength)}
	}
	return nil
}

// ToSkill converts a Spec to the legacy Skill representation. content is the
// already-loaded body (empty for metadata-only discovery). Legacy defaults are
// applied here: priority 100 and timeout 60s when unset.
func (s *Spec) ToSkill(content string) Skill {
	priority := s.Cortex.Priority
	if priority == 0 {
		priority = 100
	}
	timeout := s.Cortex.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return Skill{
		Name:                   s.Name,
		Description:            s.Description,
		Path:                   s.Path,
		Content:                content,
		Triggers:               cloneTriggers(s.Cortex.Triggers),
		Tags:                   cloneStrings(s.Cortex.Tags),
		Priority:               priority,
		Timeout:                timeout,
		Retries:                s.Cortex.Retries,
		Fallback:               s.Cortex.Fallback,
		Dependencies:           cloneStrings(s.Cortex.Dependencies),
		Metadata:               s.Metadata,
		Paths:                  cloneStrings(s.Cortex.Paths),
		WhenToUse:              s.Cortex.WhenToUse,
		AllowedTools:           cloneStrings(s.AllowedTools),
		License:                s.License,
		Compatibility:          s.Compatibility,
		DisableModelInvocation: s.DisableModelInvocation,
	}
}

// splitFrontmatter splits a markdown document into its YAML frontmatter and
// body. A leading "---" line starts the block and the next "---" line closes
// it. Files without a leading "---" have no frontmatter.
func splitFrontmatter(data []byte) (string, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, string([]byte{0xEF, 0xBB, 0xBF}))

	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", strings.TrimSpace(text), nil
	}

	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			frontmatter := strings.Join(lines[1:i], "\n")
			body := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			return frontmatter, body, nil
		}
	}
	return "", "", fmt.Errorf("frontmatter was not closed with '---'")
}

// defaultSkillName derives a name when frontmatter omits it: the parent
// directory for SKILL.md, otherwise the file stem.
func defaultSkillName(path string) string {
	base := filepath.Base(path)
	if strings.EqualFold(base, SkillFileName) {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func isCortexExtensionKey(key string) bool {
	for _, k := range legacyExtensionKeys {
		if k == key {
			return true
		}
	}
	return false
}

func applyCortexField(c *CortexMetadata, key string, v interface{}) string {
	switch key {
	case "triggers":
		triggers, ok := parseTriggersValue(v)
		if !ok {
			return "field \"triggers\" must be a list of {type, pattern} mappings"
		}
		c.Triggers = triggers
	case "tags":
		c.Tags = asStringSlice(v)
	case "priority":
		if n, ok := asInt(v); ok {
			c.Priority = n
		} else {
			return "field \"priority\" must be an integer"
		}
	case "dependencies":
		c.Dependencies = asStringSlice(v)
	case "paths":
		c.Paths = asStringSlice(v)
	case "when_to_use":
		if s, ok := asString(v); ok {
			c.WhenToUse = s
		} else {
			return "field \"when_to_use\" must be a string"
		}
	case "timeout":
		if d, ok := asDuration(v); ok {
			c.Timeout = d
		} else {
			return "field \"timeout\" must be a duration string (e.g. \"30s\") or a number of seconds"
		}
	case "retries":
		if n, ok := asInt(v); ok {
			c.Retries = n
		} else {
			return "field \"retries\" must be an integer"
		}
	case "fallback":
		if s, ok := asString(v); ok {
			c.Fallback = s
		} else {
			return "field \"fallback\" must be a string"
		}
	}
	return ""
}

func parseTriggersValue(v interface{}) ([]Trigger, bool) {
	items, ok := asSlice(v)
	if !ok {
		return nil, false
	}
	triggers := make([]Trigger, 0, len(items))
	for _, item := range items {
		m, ok := asMap(item)
		if !ok {
			continue
		}
		t := Trigger{}
		if s, ok := asString(m["type"]); ok {
			t.Type = strings.TrimSpace(s)
		}
		if s, ok := asString(m["pattern"]); ok {
			t.Pattern = strings.TrimSpace(s)
		}
		if t.Type != "" && t.Pattern != "" {
			triggers = append(triggers, t)
		}
	}
	return triggers, true
}

func asMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(m))
		for k, val := range m {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func asSlice(v interface{}) ([]interface{}, bool) {
	switch s := v.(type) {
	case []interface{}:
		return s, true
	case []string:
		out := make([]interface{}, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return out, true
	default:
		return nil, false
	}
}

func asString(v interface{}) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case fmt.Stringer:
		return s.String(), true
	default:
		return "", false
	}
}

func asStringSlice(v interface{}) []string {
	items, ok := asSlice(v)
	if !ok {
		if s, ok := asString(v); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return []string{trimmed}
			}
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := asString(item); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func asBool(v interface{}) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return false, false
		}
		return parsed, true
	default:
		return false, false
	}
}

func asDuration(v interface{}) (time.Duration, bool) {
	switch d := v.(type) {
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(d))
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return time.Duration(d) * time.Second, true
	case int64:
		return time.Duration(d) * time.Second, true
	case float64:
		return time.Duration(d * float64(time.Second)), true
	default:
		return 0, false
	}
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func cloneTriggers(in []Trigger) []Trigger {
	if len(in) == 0 {
		return nil
	}
	return append([]Trigger(nil), in...)
}
