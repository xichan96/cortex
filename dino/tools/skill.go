package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
)

type SkillTool struct {
	skills map[string]*skills.Skill
}

func NewSkillTool(list []*skills.Skill) types.Tool {
	skillMap := make(map[string]*skills.Skill)
	for _, s := range list {
		skillMap[s.Name] = s
	}
	return &SkillTool{skills: skillMap}
}

func (t *SkillTool) Name() string {
	return "skill"
}

func (t *SkillTool) Description() string {
	return "Use a skill to accomplish specific tasks. Each skill contains specialized instructions and tools."
}

func (t *SkillTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"skill_name": map[string]interface{}{
				"type":        "string",
				"description": "The name of the skill to use",
			},
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Input for the skill task",
			},
		},
		"required": []string{"skill_name"},
	}
}

func (t *SkillTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	skillName := firstString(input, "skill_name", "skillName", "name")
	if skillName == "" {
		return nil, fmt.Errorf("skill_name is required")
	}

	skill, exists := t.skills[skillName]
	if !exists {
		for k, s := range t.skills {
			if strings.EqualFold(k, skillName) {
				skill, exists = s, true
				break
			}
		}
	}
	if !exists {
		return nil, fmt.Errorf("skill not found: %s", skillName)
	}

	inputText := firstString(input, "input", "task", "query")
	skillDir := filepath.Dir(skill.Path)
	header := fmt.Sprintf("Skill: %s\nSkill directory (resolve relative paths from here): %s\n\n", skill.Name, skillDir)
	if inputText == "" {
		return header + skill.Content, nil
	}
	return fmt.Sprintf("%s%s\n\nInput: %s", header, skill.Content, inputText), nil
}

func (t *SkillTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		ToolType: "builtin",
		Priority: 5,
	}
}

func firstString(input map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := input[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}
