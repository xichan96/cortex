package skills

import "time"

// Trigger represents a skill activation trigger
type Trigger struct {
	Type    string `yaml:"type"`
	Pattern string `yaml:"pattern"`
}

// Skill represents a loaded AgentSkill
type Skill struct {
	Name         string                 `yaml:"name"`
	Description  string                 `yaml:"description"`
	Path         string                 `yaml:"-"` // Absolute path to the SKILL.md file
	Content      string                 `yaml:"-"` // The markdown content (instructions)
	Triggers     []Trigger              `yaml:"triggers"`
	Tags         []string               `yaml:"tags"`
	Priority     int                    `yaml:"priority"`
	Timeout      time.Duration          `yaml:"timeout"`
	Retries      int                    `yaml:"retries"`
	Fallback     string                 `yaml:"fallback"`
	Dependencies []string               `yaml:"dependencies"`
	Metadata     map[string]interface{} `yaml:"metadata,omitempty"`
	Paths        []string               `yaml:"paths"`
	WhenToUse    string                 `yaml:"when_to_use"`
	AllowedTools []string               `yaml:"allowed_tools"`
}

// ExecutionMode defines how a group of skills should be executed
type ExecutionMode string

const (
	ModeParallel ExecutionMode = "parallel"
	ModeSerial   ExecutionMode = "serial"
)

// ExecutionStep represents a single step in an execution plan
type ExecutionStep struct {
	Mode   ExecutionMode
	Skills []*Skill
}

// ExecutionPlan represents the ordered execution of skills
type ExecutionPlan struct {
	Steps []ExecutionStep
}
