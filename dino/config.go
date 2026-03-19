package dino

import (
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/agent"
)

type Config struct {
	DefaultModel          string                   `yaml:"default_model"`
	DefaultProvider       string                   `yaml:"default_provider"`
	Temperature           float32                  `yaml:"temperature"`
	MaxTokens             int                      `yaml:"max_tokens"`
	TopP                  float32                  `yaml:"top_p"`
	Timeout               time.Duration            `yaml:"timeout"`
	SystemPrompt          string                   `yaml:"system_prompt"`
	MaxIterations         int                      `yaml:"max_iterations"`
	MaxSessions           int                      `yaml:"max_sessions"`
	ToolExecutionTimeout  time.Duration            `yaml:"tool_execution_timeout"`
	ToolTimeouts          map[string]time.Duration `yaml:"tool_timeouts"`
	ToolTimeoutCalculator func(toolName string, input map[string]interface{}) time.Duration
	LoopDetection         LoopDetectionConfig    `yaml:"loop_detection"`
	Budget                BudgetConfig           `yaml:"budget"`
	Tools                 ToolConfig             `yaml:"tools"`
	Permission            map[string]interface{} `yaml:"permission"`
	WorkspaceRoot         string                 `yaml:"workspace_root"`
	Skills                SkillsConfig           `yaml:"skills"`
	Provider              ProviderConfig         `yaml:"provider"`
	PlannerMode           PlannerModeConfig      `yaml:"planner_mode"`
	Memory                MemoryConfig           `yaml:"memory"`
	Subagent              agent.SubagentConfig   `yaml:"subagent"`
	MCP                   MCPConfig              `yaml:"mcp"`
}

type MemoryConfig struct {
	EnableCompress     bool   `yaml:"enable_compress"`
	MaxHistoryMessages int    `yaml:"max_history_messages"`
	CompressThreshold  int    `yaml:"compress_threshold"`
	KeepRecentCount    int    `yaml:"keep_recent_count"`
	PersistDirectory   string `yaml:"persist_directory"`
	PersistEnabled     bool   `yaml:"persist_enabled"`
	Type               string `yaml:"type"` // "memory" or "sqlite"
}

type PlannerModeConfig struct {
	Enabled     bool   `yaml:"enabled"`
	PromptPlan  string `yaml:"prompt_plan"`
	AutoApprove bool   `yaml:"auto_approve"`
}

type ProviderConfig struct {
	Type    string            `yaml:"type"`
	APIKey  string            `yaml:"api_key"`
	BaseURL string            `yaml:"base_url"`
	Models  map[string]string `yaml:"models"`
	Headers map[string]string `yaml:"headers"`
}

type SkillsConfig struct {
	Path     string `yaml:"path"`
	AutoLoad bool   `yaml:"auto_load"`
}

type ToolConfig struct {
	Profile          string   `yaml:"profile"`
	Allowed          []string `yaml:"allowed"`
	ApprovalRequired []string `yaml:"approval_required"`
	Denied           []string `yaml:"denied"`
}

type LoopDetectionConfig struct {
	Enabled             bool    `yaml:"enabled"`
	MaxRepeats          int     `yaml:"max_repeats"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	InputMaxRepeats     int     `yaml:"input_max_repeats"`
}

type MCPConfig struct {
	Enabled bool                       `yaml:"enabled"`
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

type MCPServerConfig struct {
	Type    string            `yaml:"type"` // "remote" | "stdio"
	URL     string            `yaml:"url,omitempty"`
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Timeout time.Duration     `yaml:"timeout,omitempty"`
	OAuth   *OAuthConfig      `yaml:"oauth,omitempty"`
	Enabled bool              `yaml:"enabled"`
}

type OAuthConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	Scope        string `yaml:"scope"`
}

type BudgetConfig struct {
	Enabled      bool  `yaml:"enabled"`
	MaxTokens    int   `yaml:"max_tokens"`
	MaxToolCalls int   `yaml:"max_tool_calls"`
	MaxTimeMs    int64 `yaml:"max_time_ms"`
}

func DefaultConfig() *Config {
	return &Config{
		DefaultModel:         "gpt-4o-mini",
		DefaultProvider:      "openai",
		Temperature:          0.7,
		MaxTokens:            4096,
		TopP:                 1.0,
		Timeout:              30 * time.Second,
		SystemPrompt:         "You are a helpful AI assistant.",
		MaxIterations:        10,
		MaxSessions:          100,
		ToolExecutionTimeout: 60 * time.Second,
		ToolTimeouts: map[string]time.Duration{
			"bash":       120 * time.Second,
			"web_fetch":  30 * time.Second,
			"web_search": 45 * time.Second,
			"glob":       10 * time.Second,
			"grep":       30 * time.Second,
			"read_file":  15 * time.Second,
			"write_file": 30 * time.Second,
			"edit_file":  15 * time.Second,
		},
		LoopDetection: LoopDetectionConfig{
			Enabled:             true,
			MaxRepeats:          5,
			SimilarityThreshold: 0.8,
		},
		Budget: BudgetConfig{
			Enabled:      true,
			MaxTokens:    100000,
			MaxToolCalls: 50,
			MaxTimeMs:    300000,
		},
		Tools: ToolConfig{
			Profile: "coding",
			Allowed: []string{
				"read_file",
				"write_file",
				"edit_file",
				"glob",
				"grep",
				"bash",
				"list_directory",
			},
			ApprovalRequired: []string{
				"bash",
				"write_file",
				"edit_file",
			},
		},
		Skills: SkillsConfig{
			Path:     "",
			AutoLoad: true,
		},
		Provider: ProviderConfig{
			Type:    "openai",
			BaseURL: "https://api.openai.com/v1",
			Models: map[string]string{
				"default": "gpt-4o-mini",
			},
		},
		WorkspaceRoot: ".",
		PlannerMode: PlannerModeConfig{
			Enabled:     false,
			PromptPlan:  "First, analyze the task and create a step-by-step plan. List the tools you will use in order.",
			AutoApprove: false,
		},
		Memory: MemoryConfig{
			EnableCompress:     true,
			MaxHistoryMessages: 100,
			CompressThreshold:  50,
			KeepRecentCount:    10,
			PersistDirectory:   "./dino_sessions",
			PersistEnabled:     false,
			Type:               "memory",
		},
		Subagent: agent.SubagentConfig{
			Enabled:          true,
			TriggerOnKeyword: true,
			Triggers: []agent.SubagentTrigger{
				{
					AgentName: "general",
					Keywords:  []string{"research", "analyze", "investigate", "study", "研究", "分析", "调查"},
					Patterns:  []string{`(?i)(research|analyze|investigate|study)\s+(about|on|the)`, `(?i)what\s+is\s+.*about`},
					Priority:  5,
				},
			},
		},
		MCP: MCPConfig{
			Enabled: false,
			Servers: make(map[string]MCPServerConfig),
		},
	}
}

type LLMProviderFactory func(config *Config) (types.LLMProvider, error)

var (
	providerRegistry = make(map[string]LLMProviderFactory)
	registryMu       sync.RWMutex
)

func RegisterLLMProvider(name string, factory LLMProviderFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	providerRegistry[name] = factory
}

func init() {
	RegisterLLMProvider("openai", func(cfg *Config) (types.LLMProvider, error) {
		return llm.NewOpenAIClient(llm.OpenAIOptions{
			APIKey:  cfg.Provider.APIKey,
			BaseURL: cfg.Provider.BaseURL,
			Model:   cfg.DefaultModel,
		})
	})
	RegisterLLMProvider("deepseek", func(cfg *Config) (types.LLMProvider, error) {
		return llm.NewDeepSeekClient(llm.DeepSeekOptions{
			APIKey: cfg.Provider.APIKey,
			Model:  cfg.DefaultModel,
		})
	})
	RegisterLLMProvider("volce", func(cfg *Config) (types.LLMProvider, error) {
		return llm.NewVolceClient(llm.VolceOptions{
			APIKey: cfg.Provider.APIKey,
			Model:  cfg.DefaultModel,
		})
	})
}

func createLLMProvider(cfg *Config) (types.LLMProvider, error) {
	registryMu.RLock()
	factory, exists := providerRegistry[cfg.Provider.Type]
	registryMu.RUnlock()
	if !exists {
		return nil, ErrProviderNotFound
	}
	return factory(cfg)
}

type ProviderError string

func (e ProviderError) Error() string {
	return string(e)
}

const ErrProviderNotFound ProviderError = "provider not found"

func GetRegisteredProviders() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	providers := make([]string, 0, len(providerRegistry))
	for name := range providerRegistry {
		providers = append(providers, name)
	}
	return providers
}

func ClearProviderRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	providerRegistry = make(map[string]LLMProviderFactory)
}
