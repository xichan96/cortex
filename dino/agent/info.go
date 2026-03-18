package agent

import (
	"github.com/xichan96/cortex/dino/permission"
)

type Info struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Mode        Mode                   `json:"mode"`
	Native      bool                   `json:"native,omitempty"`
	Hidden      bool                   `json:"hidden,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
	Temperature *float64               `json:"temperature,omitempty"`
	Color       string                 `json:"color,omitempty"`
	Permission  permission.Ruleset     `json:"permission"`
	Model       *ModelInfo             `json:"model,omitempty"`
	Variant     string                 `json:"variant,omitempty"`
	Prompt      string                 `json:"prompt,omitempty"`
	Options     map[string]interface{} `json:"options,omitempty"`
	Steps       *int                   `json:"steps,omitempty"`
}

type ModelInfo struct {
	ModelID    string `json:"model_id"`
	ProviderID string `json:"provider_id"`
}

type Mode string

const (
	ModeSubagent Mode = "subagent"
	ModePrimary  Mode = "primary"
	ModeAll      Mode = "all"
)

type Config struct {
	Agents map[string]*AgentConfig `json:"agents,omitempty"`
}

type AgentConfig struct {
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Mode        Mode                   `json:"mode,omitempty"`
	Native      bool                   `json:"native,omitempty"`
	Hidden      bool                   `json:"hidden,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
	Temperature *float64               `json:"temperature,omitempty"`
	Color       string                 `json:"color,omitempty"`
	Model       string                 `json:"model,omitempty"`
	Variant     string                 `json:"variant,omitempty"`
	Prompt      string                 `json:"prompt,omitempty"`
	Options     map[string]interface{} `json:"options,omitempty"`
	Steps       *int                   `json:"steps,omitempty"`
	Permission  map[string]interface{} `json:"permission,omitempty"`
	Disable     bool                   `json:"disable,omitempty"`
}

func DefaultAgents() map[string]*Info {
	return map[string]*Info{
		"build": {
			Name:        "build",
			Description: "The default agent. Executes tools based on configured permissions.",
			Mode:        ModePrimary,
			Native:      true,
			Permission:  permission.BuildAgentRuleset(permission.ModeBuild),
			Options:     make(map[string]interface{}),
		},
		"plan": {
			Name:        "plan",
			Description: "Plan mode. Disallows all edit tools.",
			Mode:        ModePrimary,
			Native:      true,
			Permission:  permission.BuildAgentRuleset(permission.ModePlan),
			Options:     make(map[string]interface{}),
		},
		"general": {
			Name:        "general",
			Description: "General-purpose agent for researching complex questions and executing multi-step tasks.",
			Mode:        ModeSubagent,
			Native:      true,
			Permission:  permission.BuildAgentRuleset(permission.ModeGeneral),
			Options:     make(map[string]interface{}),
		},
		"explore": {
			Name:        "explore",
			Description: "Fast agent specialized for exploring codebases.",
			Mode:        ModeSubagent,
			Native:      true,
			Permission:  permission.BuildAgentRuleset(permission.ModeExplore),
			Prompt:      explorePrompt,
			Options:     make(map[string]interface{}),
		},
		"compaction": {
			Name:        "compaction",
			Description: "Compaction mode. Only allows read operations.",
			Mode:        ModePrimary,
			Native:      true,
			Hidden:      true,
			Prompt:      compactionPrompt,
			Permission:  permission.BuildAgentRuleset(permission.ModeCompaction),
			Options:     make(map[string]interface{}),
		},
		"title": {
			Name:        "title",
			Description: "Title generation agent.",
			Mode:        ModePrimary,
			Native:      true,
			Hidden:      true,
			Prompt:      titlePrompt,
			Temperature: floatPtr(0.5),
			Permission:  permission.BuildAgentRuleset(permission.ModeCompaction),
			Options:     make(map[string]interface{}),
		},
		"summary": {
			Name:        "summary",
			Description: "Summary generation agent.",
			Mode:        ModePrimary,
			Native:      true,
			Hidden:      true,
			Prompt:      summaryPrompt,
			Permission:  permission.BuildAgentRuleset(permission.ModeCompaction),
			Options:     make(map[string]interface{}),
		},
	}
}

func floatPtr(v float64) *float64 {
	return &v
}

const explorePrompt = `You are a fast, efficient code explorer. Your goal is to quickly find relevant information and patterns in the codebase.

Guidelines:
- Use glob to find files by patterns
- Use grep to search for keywords and patterns
- Use read to examine specific files
- Be thorough but efficient - stop when you have enough information
- Report your findings clearly with file paths and line numbers`

const compactionPrompt = `You are a session compaction agent. Your task is to summarize the session history while preserving important context.

Guidelines:
- Summarize tool calls and their results
- Preserve important decisions and their rationale
- Remove redundant or unnecessary messages
- Keep the session compressed but informative`

const titlePrompt = `Generate a concise, descriptive title for the conversation. The title should be 3-6 words that capture the main topic or task.`

const summaryPrompt = `Generate a summary of the conversation. Include:
- Main goals and accomplishments
- Key decisions made
- Important files modified
- Remaining tasks or follow-ups`

type SubagentConfig struct {
	Enabled          bool              `yaml:"enabled"`
	TriggerOnKeyword bool              `yaml:"trigger_on_keyword"`
	Triggers         []SubagentTrigger `yaml:"triggers"`
}

type SubagentTrigger struct {
	AgentName string   `yaml:"agent_name"`
	Keywords  []string `yaml:"keywords"`
	Patterns  []string `yaml:"patterns"`
	Priority  int      `yaml:"priority"`
}
