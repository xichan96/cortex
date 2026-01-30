package skills

import (
	"fmt"
	"strings"
)

// BuildSystemPromptInjection generates the skills section for the system prompt.
// It follows the Lazy Load pattern: listing available skills and instructing the agent to read definition files.
func BuildSystemPromptInjection(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Available Skills\n")
	sb.WriteString("You have access to the following skills. To use a skill, you MUST first read its definition file.\n\n")

	for _, skill := range skills {
		sb.WriteString(fmt.Sprintf("- Name: %s\n", skill.Name))
		if skill.Description != "" {
			sb.WriteString(fmt.Sprintf("  Description: %s\n", skill.Description))
		}
		sb.WriteString(fmt.Sprintf("  Definition File: %s\n", skill.Path))
		sb.WriteString("\n")
	}

	sb.WriteString("## Skill Usage Protocol\n")
	sb.WriteString("1. Identify relevant skill from the list based on the user's request.\n")
	sb.WriteString("2. Use the `read_file` tool to read the content of the 'Definition File'.\n")
	sb.WriteString("3. Follow the instructions found in the SKILL.md file strictly.\n")
	sb.WriteString("4. Do not guess the skill capabilities; always read the definition first.\n")

	return sb.String()
}
