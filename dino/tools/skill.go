package tools

import (
	"context"
	"fmt"

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
	skillName, ok := input["skill_name"].(string)
	if !ok || skillName == "" {
		return nil, fmt.Errorf("skill_name is required")
	}

	skill, exists := t.skills[skillName]
	if !exists {
		return nil, fmt.Errorf("skill not found: %s", skillName)
	}

	inputText, _ := input["input"].(string)
	if inputText == "" {
		return skill.Content, nil
	}
	return fmt.Sprintf("Skill: %s\n\n%s\n\nInput: %s", skill.Name, skill.Content, inputText), nil
}

func (t *SkillTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		ToolType: "builtin",
		Priority: 5,
	}
}
