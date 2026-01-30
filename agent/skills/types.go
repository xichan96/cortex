package skills

// Skill represents a loaded AgentSkill
type Skill struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Path        string                 `yaml:"-"` // Absolute path to the SKILL.md file
	Metadata    map[string]interface{} `yaml:"metadata,omitempty"`
}
