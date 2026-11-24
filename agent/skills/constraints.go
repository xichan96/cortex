package skills

import (
	"fmt"
	"strings"
)

// AllowedTools returns the tools declared by the skill's allowed-tools field.
// The returned slice is a copy. nil means the skill declares no restriction.
func (e *Entry) AllowedTools() []string {
	if e == nil || e.Spec == nil {
		return nil
	}
	return append([]string(nil), e.Spec.AllowedTools...)
}

// Paths returns the Glob patterns declared by the skill's paths field. The
// returned slice is a copy. nil means the skill is not path-scoped.
func (e *Entry) Paths() []string {
	if e == nil || e.Spec == nil {
		return nil
	}
	return append([]string(nil), e.Spec.Cortex.Paths...)
}

// SkillPolicy is the enforceable constraint set a skill declares (design 6.5):
// the tools a skill may use while it is active and the Glob patterns that decide
// when it is advertisable. An empty field means that dimension is unrestricted.
type SkillPolicy struct {
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Paths        []string `json:"paths,omitempty"`
}

// FromEntry extracts the policy from a discovered skill. A nil entry yields the
// zero policy. Returned slices are copies; mutating them does not affect the
// entry.
func FromEntry(entry *Entry) SkillPolicy {
	return SkillPolicy{
		AllowedTools: entry.AllowedTools(),
		Paths:        entry.Paths(),
	}
}

// Empty reports whether the policy constrains nothing.
func (p SkillPolicy) Empty() bool {
	return len(p.AllowedTools) == 0 && len(p.Paths) == 0
}

// FilterToolNames returns the subset of available tool names permitted by an
// allowed-tools declaration, preserving the order of available.
//
// An empty allowed list means "no restriction": available is returned as a
// copy and unknown is nil. Allowed names that do not exist in available are
// ignored by the filter and reported separately through unknown (deduplicated,
// in declaration order) so hosts can warn about likely typos instead of
// silently ignoring them (design 6.5).
func FilterToolNames(available, allowed []string) (filtered, unknown []string) {
	if len(allowed) == 0 {
		return append([]string(nil), available...), nil
	}

	permitted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		permitted[name] = struct{}{}
	}

	filtered = make([]string, 0, len(available))
	present := make(map[string]struct{}, len(available))
	for _, name := range available {
		present[name] = struct{}{}
		if _, ok := permitted[name]; ok {
			filtered = append(filtered, name)
		}
	}

	reported := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		if _, ok := present[name]; ok {
			continue
		}
		if _, ok := reported[name]; ok {
			continue
		}
		reported[name] = struct{}{}
		unknown = append(unknown, name)
	}
	return filtered, unknown
}

// AdvertisableForPaths returns the entries that may be advertised to the model
// given the currently active file paths.
//
// Skills with disable-model-invocation are never included (they stay reachable
// through /skill:name). Skills that declare no paths are always advertisable.
// An empty activePaths means "no path context", so every advertisable entry is
// returned. Otherwise a path-scoped skill is included when at least one active
// path matches one of its doublestar Glob patterns (see MatchPathPatterns).
func AdvertisableForPaths(index *Index, activePaths []string) []*Entry {
	if index == nil {
		return nil
	}
	entries := index.Advertisable()
	if len(activePaths) == 0 {
		return entries
	}

	out := make([]*Entry, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Paths()) == 0 {
			out = append(out, entry)
			continue
		}
		for _, active := range activePaths {
			if entry.MatchesPath(active) {
				out = append(out, entry)
				break
			}
		}
	}
	return out
}

// FormatAvailableSkillsDetailed renders the <available_skills> block like
// FormatAvailableSkills and additionally exposes the skill's constraint
// metadata (allowed-tools and paths) when present. Skills with
// disable-model-invocation are omitted.
//
// The plain FormatAvailableSkills output is unchanged; this variant exists so
// hosts and models can see constraints without loading the skill body.
func FormatAvailableSkillsDetailed(entries []*Entry) string {
	advertisable := make([]*Entry, 0, len(entries))
	for _, entry := range entries {
		if entry != nil && !entry.DisableModelInvocation {
			advertisable = append(advertisable, entry)
		}
	}
	if len(advertisable) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<available_skills>\n")
	for _, entry := range advertisable {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", escapeSkillXML(entry.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", escapeSkillXML(entry.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", escapeSkillXML(entry.Path))
		if tools := entry.AllowedTools(); len(tools) > 0 {
			fmt.Fprintf(&b, "    <allowed_tools>%s</allowed_tools>\n", escapeSkillXML(strings.Join(tools, ", ")))
		}
		if paths := entry.Paths(); len(paths) > 0 {
			fmt.Fprintf(&b, "    <paths>%s</paths>\n", escapeSkillXML(strings.Join(paths, ", ")))
		}
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}
