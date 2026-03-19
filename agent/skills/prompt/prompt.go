package prompt

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed injection_header.txt
var injectionHeader string

//go:embed usage_protocol.txt
var usageProtocol string

//go:embed triggers_header.txt
var triggersHeader string

//go:embed triggers_footer.txt
var triggersFooter string

type Entry struct {
	Name, Description, Path string
}

type Trigger struct {
	Type    string
	Pattern string
}

type EntryWithTriggers struct {
	Name, Description string
	Triggers          []Trigger
}

func BuildSystemPromptInjection(skills []Entry) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n")
	sb.WriteString(injectionHeader)
	for _, skill := range skills {
		sb.WriteString(fmt.Sprintf("- Name: %s\n", skill.Name))
		if skill.Description != "" {
			sb.WriteString(fmt.Sprintf("  Description: %s\n", skill.Description))
		}
		sb.WriteString(fmt.Sprintf("  Definition File: %s\n", skill.Path))
		sb.WriteString("\n")
	}
	sb.WriteString(usageProtocol)
	return sb.String()
}

func BuildSystemPromptInjectionWithTriggers(skills []*EntryWithTriggers) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n")
	sb.WriteString(triggersHeader)
	for _, skill := range skills {
		sb.WriteString(fmt.Sprintf("### %s\n", skill.Name))
		sb.WriteString(fmt.Sprintf("%s\n", skill.Description))
		if len(skill.Triggers) > 0 {
			sb.WriteString("Triggers: ")
			var parts []string
			for _, t := range skill.Triggers {
				parts = append(parts, fmt.Sprintf("%s:%s", t.Type, t.Pattern))
			}
			sb.WriteString(strings.Join(parts, ", "))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(triggersFooter)
	return sb.String()
}
