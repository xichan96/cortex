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
	WhenToUse               string
	Paths                   []string
	AllowedTools            []string
}

type Trigger struct {
	Type    string
	Pattern string
}

type EntryWithTriggers struct {
	Name, Description string
	Triggers          []Trigger
	WhenToUse         string
	Paths             []string
	AllowedTools      []string
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
		if skill.WhenToUse != "" {
			sb.WriteString(fmt.Sprintf("  When to use: %s\n", skill.WhenToUse))
		}
		if len(skill.Paths) > 0 {
			sb.WriteString(fmt.Sprintf("  Active when path matches: %s\n", strings.Join(skill.Paths, ", ")))
		}
		if len(skill.AllowedTools) > 0 {
			sb.WriteString(fmt.Sprintf("  Allowed tools: %s\n", strings.Join(skill.AllowedTools, ", ")))
		}
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
		if skill.WhenToUse != "" {
			sb.WriteString(fmt.Sprintf("When to use: %s\n", skill.WhenToUse))
		}
		if len(skill.Paths) > 0 {
			sb.WriteString(fmt.Sprintf("Active when path matches: %s\n", strings.Join(skill.Paths, ", ")))
		}
		if len(skill.AllowedTools) > 0 {
			sb.WriteString(fmt.Sprintf("Allowed tools: %s\n", strings.Join(skill.AllowedTools, ", ")))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(triggersFooter)
	return sb.String()
}
