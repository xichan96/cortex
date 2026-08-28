package agent

import (
	"embed"
	"strings"

	"github.com/xichan96/cortex/dino/permission"
)

//go:embed prompt/*.txt
var promptsFS embed.FS

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

func loadPrompt(name string) string {
	data, err := promptsFS.ReadFile("prompt/" + name + ".txt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
			Prompt:      loadPrompt("general"),
			Options:     make(map[string]interface{}),
		},
		"compaction": {
			Name:        "compaction",
			Description: "Compaction mode. Only allows read operations.",
			Mode:        ModePrimary,
			Native:      true,
			Hidden:      true,
			Prompt:      loadPrompt("compaction"),
			Permission:  permission.BuildAgentRuleset(permission.ModeCompaction),
			Options:     make(map[string]interface{}),
		},
		"title": {
			Name:        "title",
			Description: "Title generation agent.",
			Mode:        ModePrimary,
			Native:      true,
			Hidden:      true,
			Prompt:      loadPrompt("title"),
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
			Prompt:      loadPrompt("summary"),
			Permission:  permission.BuildAgentRuleset(permission.ModeCompaction),
			Options:     make(map[string]interface{}),
		},
	}
}

func floatPtr(v float64) *float64 {
	return &v
}

type SubagentConfig struct {
	Enabled              bool              `yaml:"enabled"`
	TriggerOnKeyword     bool              `yaml:"trigger_on_keyword"`
	ReplayToParentMemory bool              `yaml:"replay_to_parent_memory"`
	MaxHistoryMessages   int               `yaml:"max_history_messages"`
	Triggers             []SubagentTrigger `yaml:"triggers"`

	// —— 结果结构化/完成通知（设计 §5.4，S1 引入；B 阶段字段见 S3）——
	// NotifyCompletion B 阶段：完成是否写 mailbox + 发事件，默认 true。
	NotifyCompletion bool `yaml:"notify_completion"`
	// CompletionMaxRunes 信封截断上限，默认 2000（≈1000 tokens）。
	CompletionMaxRunes int `yaml:"completion_max_runes"`
	// DelegateReturnMode delegate_to_agent 返回形态（设计 §14 遗留点 1，P2.2）：
	// "envelope"（默认）返回 *DelegateResult 信封；"string" 返回裸字符串
	// （result.Output，S1 之前的兼容形态）。string 模式下 FilesChanged/Status/Usage
	// 等信息不进工具返回值，是纯兼容开关。非法值/空串回退 envelope。
	DelegateReturnMode string `yaml:"delegate_return_mode"`
}

// DelegateReturnMode 合法值（设计 §14 遗留点 1，P2.2）。
const (
	// DelegateReturnModeEnvelope 返回 *DelegateResult 信封（S1 起的默认行为）。
	DelegateReturnModeEnvelope = "envelope"
	// DelegateReturnModeString 返回裸字符串 result.Output（S1 之前的兼容行为）。
	DelegateReturnModeString = "string"
)

// DelegateReturnModeOrDefault 解析 DelegateReturnMode 配置，空串/非法值回退 envelope
// （防御：旧配置没有该字段，或手写 YAML 拼错时保持 S1 行为不炸）。
func DelegateReturnModeOrDefault(mode string) string {
	switch mode {
	case DelegateReturnModeEnvelope, DelegateReturnModeString:
		return mode
	default:
		return DelegateReturnModeEnvelope
	}
}

type SubagentTrigger struct {
	AgentName string   `yaml:"agent_name"`
	Keywords  []string `yaml:"keywords"`
	Patterns  []string `yaml:"patterns"`
	Priority  int      `yaml:"priority"`
}
