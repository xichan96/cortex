package dino

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/agent"
	"github.com/xichan96/cortex/dino/mem"
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
	ToolTimeoutCalculator func(toolName string, input map[string]interface{}) time.Duration `json:"-" yaml:"-"`
	LoopDetection         LoopDetectionConfig    `yaml:"loop_detection"`
	Budget                BudgetConfig           `yaml:"budget"`
	Tools                 ToolConfig             `yaml:"tools"`
	Permission            map[string]interface{} `yaml:"permission"`
	WorkspaceRoot         string                 `yaml:"workspace_root"`
	Skills                SkillsConfig           `yaml:"skills"`
	Provider              ProviderConfig         `yaml:"provider"`
	PlannerMode           PlannerModeConfig      `yaml:"planner_mode"`
	Memory                MemoryConfig           `yaml:"memory"`
	PromptCaching         PromptCachingConfig    `yaml:"prompt_caching"`
	LongTermMemory        MemLongTermConfig      `yaml:"long_term_memory"`
	Subagent              agent.SubagentConfig   `yaml:"subagent"`
	MCP                   MCPConfig              `yaml:"mcp"`
}

type MemoryConfig struct {
	EnableCompress     bool   `yaml:"enable_compress"`
	EnableLLMCompress  bool   `yaml:"enable_llm_compress"` // 压缩是否走 LLM 摘要（Hybrid 包裹）；false 退回 DeterministicCompact。与 EnableCompress（是否压缩）语义正交
	MaxHistoryMessages int    `yaml:"max_history_messages"`
	MaxBudgetTokens    int    `yaml:"max_budget_tokens"`
	CompactAfterTurns  int    `yaml:"compact_after_turns"`
	CompressThreshold  int    `yaml:"compress_threshold"` // message-count gate for async compress; if <=0, CreateSession uses budget.max_tool_calls/2 then agent default
	KeepRecentCount    int    `yaml:"keep_recent_count"`
	PersistDirectory   string `yaml:"persist_directory"`
	PersistFileName    string `yaml:"persist_file_name"`
	PersistEnabled     bool   `yaml:"persist_enabled"`
	Type               string `yaml:"type"` // "memory" or "sqlite"
	// CompactionPrefix enables prefix-preserving compaction (P3.1 · prompt
	// caching Step 4): trimHistoryToTokenBudget keeps a head cache anchor and
	// preserves the recent tail verbatim, replacing the middle with a summary,
	// so prompt-cache segments 1-2 (system+tools) survive compaction. Default
	// off (conservative): the trim output structure changes when on.
	CompactionPrefix bool `yaml:"compaction_prefix"`
	// CacheAnchorTokens caps the head-cache-anchor budget (tokens). 0 = no
	// anchor (the head is trimmed like today). Default 0. Suggest ≤30% of
	// MaxBudgetTokens so the tail keeps enough room to anchor segment 3 (R5).
	CacheAnchorTokens int `yaml:"cache_anchor_tokens"`
}

// PromptCachingConfig controls provider prompt caching. nil sub-fields mean
// "use the provider default" (see types.DefaultPromptCacheOptions). This lets
// a partial YAML block (e.g. only `enabled: false`) override just that field
// instead of silently zeroing the other toggles.
type PromptCachingConfig struct {
	Enabled          bool  `yaml:"enabled"`             // default true (pure cost optimization)
	SystemBreakpoint *bool `yaml:"system_breakpoint,omitempty"`
	ToolsBreakpoint  *bool `yaml:"tools_breakpoint,omitempty"`
	HistoryEveryN    *int  `yaml:"history_every_n,omitempty"` // history breakpoint budget (≤ remaining of 4); 0 = none
	MinCacheTokens   *int  `yaml:"min_cache_tokens,omitempty"`
}

// MemLongTermConfig 长期记忆配置段。定义在 dino/mem 包（避免 import cycle），
// 这里按原样引用。
type MemLongTermConfig = mem.MemLongTermConfig

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
	Profile                    string   `yaml:"profile"`
	Allowed                    []string `yaml:"allowed"`
	ApprovalRequired           []string `yaml:"approval_required"`
	Denied                     []string `yaml:"denied"`
	MaxToolParallelism         int      `yaml:"max_tool_parallelism,omitempty"`          // 0=默认 max(4, GOMAXPROCS*2)；>0 用该值
	StreamBufferSize           int      `yaml:"stream_buffer_size,omitempty"`            // 0=默认 50
	ResultLimiterMaxBytes      int      `yaml:"result_limiter_max_bytes,omitempty"`      // 0=默认 120_000
	ResultLimiterMaxStringBytes int     `yaml:"result_limiter_max_string_bytes,omitempty"` // 0=默认 60_000
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
		SystemPrompt: `You are an interactive AI coding assistant. Use the tools available to help the user with software engineering tasks.

# Doing tasks
 - Read relevant code before changing it. Keep changes tightly scoped to what was requested.
 - Do not add speculative abstractions, error handling, or unrelated cleanup beyond what is asked.
 - Do not create files unless they are required to complete the task.
 - If an approach fails, diagnose the failure before switching tactics.
 - Do not add error handling, fallbacks, or validation for scenarios that can't happen. Trust internal code and framework guarantees.

# Executing actions with care
 - Carefully consider reversibility and blast radius. Local, reversible actions like editing files or running tests are usually fine.
 - Actions that delete data, push to remote, send messages, or affect shared systems require explicit user confirmation.
 - Never skip safety checks unless the user explicitly asks.`,
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
			EnableLLMCompress:  true,
			MaxHistoryMessages: 100,
			MaxBudgetTokens:    0,
			CompactAfterTurns:  0,
			CompressThreshold:  50,
			KeepRecentCount:    10,
			PersistDirectory:   "./dino_sessions",
			PersistEnabled:     false,
			Type:               "memory",
		},
		PromptCaching: PromptCachingConfig{
			Enabled: true,
		},
		LongTermMemory: MemLongTermConfig{
			Enabled:             false,
			ToolName:            "memory",
			WriteKnowledgeTag:   "memory_tool_write",
			ExposeSearchIndexes: false,
			IngestInterval:      2 * time.Minute,
			IngestBatchMax:      50,
			IngestMinNew:        2,
			EnableContentFilter: true,
			PromptMaxTokens:     2500,
			Phase2Merge:         true,
			Phase2LLMMerge:      false,
			MaxUnusedDays:       30,
			UseSameLLMForIngest: true,
			UserMergeEnabled:    false,
			DefaultUserID:       "",
		},
		Subagent: agent.SubagentConfig{
			Enabled:              true,
			MaxHistoryMessages:   48,
			NotifyCompletion:     true,
			CompletionMaxRunes:   agent.DefaultDelegateTruncatedRunes,
			DelegateReturnMode:   agent.DelegateReturnModeEnvelope,
			MaxConcurrentSpawns:  agent.DefaultMaxConcurrentSpawns,
			SpawnTimeout:         agent.DefaultSpawnTimeout,
			WakeOnCompletion:     false, // S4 灰度开关：默认关，显式开启才启用 B2 唤醒（评审 RECOMMENDED-1）
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
	RegisterLLMProvider("anthropic", func(cfg *Config) (types.LLMProvider, error) {
		return llm.NewAnthropicClient(llm.AnthropicOptions{
			APIKey:  cfg.Provider.APIKey,
			BaseURL: cfg.Provider.BaseURL,
			Model:   cfg.DefaultModel,
		})
	})
}

// ValidateConfig 在构造 DinoFactory 前做配置校验，让配置错误尽早、清晰暴露
// （而不是等到 createLLMProvider 时才报笼统的 provider not found）。
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}

	// Provider.Type 必须设置且已注册（列出支持列表帮助排错）。
	if cfg.Provider.Type == "" {
		return fmt.Errorf("config validation: llm provider type is empty (set dino.Config.Provider.Type; supported: %s)",
			strings.Join(GetRegisteredProviders(), ", "))
	}
	registryMu.RLock()
	_, exists := providerRegistry[cfg.Provider.Type]
	registryMu.RUnlock()
	if !exists {
		return fmt.Errorf("config validation: unknown llm provider type %q (supported: %s)",
			cfg.Provider.Type, strings.Join(GetRegisteredProviders(), ", "))
	}

	// DefaultModel 必须设置——各 provider client 虽有权重默认，但自定义
	// provider（代理/网关）通常需要显式模型，空值易造成"以为配了实际走默认"。
	if cfg.DefaultModel == "" {
		return fmt.Errorf("config validation: default model is empty (set dino.Config.DefaultModel)")
	}

	return nil
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

// PromptCacheOptions maps dino's PromptCachingConfig onto the shared
// types.PromptCacheOptions, starting from provider defaults so partial YAML
// overrides don't zero unrelated toggles.
func (c *Config) PromptCacheOptions() types.PromptCacheOptions {
	opts := types.DefaultPromptCacheOptions()
	if c == nil {
		return opts
	}
	opts.Enabled = c.PromptCaching.Enabled
	if c.PromptCaching.SystemBreakpoint != nil {
		opts.SystemBreakpoint = *c.PromptCaching.SystemBreakpoint
	}
	if c.PromptCaching.ToolsBreakpoint != nil {
		opts.ToolsBreakpoint = *c.PromptCaching.ToolsBreakpoint
	}
	if c.PromptCaching.HistoryEveryN != nil {
		opts.HistoryEveryN = *c.PromptCaching.HistoryEveryN
	}
	if c.PromptCaching.MinCacheTokens != nil {
		opts.MinCacheTokens = *c.PromptCaching.MinCacheTokens
	}
	return opts
}

// ConfigurePromptCache applies prompt caching to a provider that supports it.
// It is a no-op for providers that don't implement types.PromptCacheConfigurer
// (OpenAI/DeepSeek/Volce — no cache_control protocol).
func ConfigurePromptCache(provider types.LLMProvider, opts types.PromptCacheOptions) {
	if pc, ok := provider.(types.PromptCacheConfigurer); ok {
		pc.SetPromptCacheOptions(opts)
	}
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
