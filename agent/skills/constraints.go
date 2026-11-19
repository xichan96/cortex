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

// Paths returns the glob patterns declared by the skill's paths field. The
// returned slice is a copy. nil means the skill is not path-scoped.
func (e *Entry) Paths() []string {
	if e == nil || e.Spec == nil {
		return nil
	}
	return append([]string(nil), e.Spec.Cortex.Paths...)
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
