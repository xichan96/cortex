package mem

import (
	"context"
	"log/slog"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

type IngestRule struct {
	Name            string
	Action          string
	MaxTotalChars   int
	MinTotalChars   int
	PhrasesAny      []string
	MaxUserMessages int
}

type IngestContentFilter struct {
	TrivialPhrases      []string
	MinContentLength    int
	EnableContentFilter bool
}

type IngestRuntime struct {
	Rules         []IngestRule
	SystemExtra   string
	ContentFilter IngestContentFilter
}

type IngestLoopOptions struct {
	WaitReady   func(ctx context.Context) error
	StartParams func() (persistDir, sqliteFile string, interval time.Duration, err error)
	TickParams  func() (enabled bool, batchMax, minNew int, rt IngestRuntime)
	Log         *slog.Logger
	NewLLM      func(ctx context.Context, rt IngestRuntime) (types.LLMProvider, error)
}

// MemLongTermConfig 长期记忆配置段（定义在 dino/mem 内以避免 dino↔dino/mem
// 的 import cycle）。
type MemLongTermConfig struct {
	Enabled             bool          `yaml:"enabled"`                        // 总开关
	ToolName            string        `yaml:"tool_name"`                      // 默认 "memory"
	ToolDescription     string        `yaml:"tool_description"`               // 默认 defaultMemoryToolDescription
	WriteKnowledgeTag   string        `yaml:"write_knowledge_tag"`            // 默认 "memory_tool_write"
	ExposeSearchIndexes bool          `yaml:"expose_search_indexes"`          // 默认 false
	IngestInterval      time.Duration `yaml:"ingest_interval"`                // 默认 2m（≥5s，loop.go 钳制）
	IngestBatchMax      int           `yaml:"ingest_batch_max"`               // 默认 50
	IngestMinNew        int           `yaml:"ingest_min_new"`                 // 默认 2
	SystemExtra         string        `yaml:"system_extra"`                   // ingest 抽取 prompt 附加政策
	EnableContentFilter bool          `yaml:"enable_content_filter"`          // 默认 true
	PromptMaxTokens     int           `yaml:"prompt_max_tokens"`              // 默认 2500（L1 硬顶）
	Phase2Merge         bool          `yaml:"phase2_merge"`                   // 全局合并，默认 true
	Phase2LLMMerge      bool          `yaml:"phase2_llm_merge"`               // LLM 冲突消解，默认 false
	MaxUnusedDays       int           `yaml:"max_unused_days"`                // 保留/遗忘，默认 30
	UseSameLLMForIngest bool          `yaml:"use_same_llm_for_ingest"`        // 默认 true

	// UserMergeEnabled 默认 false。开启 user 全局合并：未显式归属（WithUserID）
	// 的 session 归 DefaultUserID（空则 "default"），Phase 2 迁移旧 per-session 数据
	// 到归属 user 并跨 session 去重收敛。默认关 = 行为与 per-session 现状一致。
	//
	// 单用户部署设 DefaultUserID 一个固定 uid，所有 session 共享记忆空间。
	// 多租户必须显式 WithUserID 隔离，"default" 兜底仅限单用户——同一进程内
	// 多个 user 共用 "default" 会串记忆。WithUserID 透传是后续独立任务。
	UserMergeEnabled bool `yaml:"user_merge_enabled"`
	// DefaultUserID 默认 ""：未显式 WithUserID 的 session 归属到该值；为空回退
	// 常量 "default"。仅 UserMergeEnabled=true 时生效。
	DefaultUserID string `yaml:"default_user_id"`
}

type MemoryToolOptions struct {
	SessionID         string
	PersistDir        string
	SQLiteFile        string
	Log               *slog.Logger
	ToolName          string
	WriteKnowledgeTag string
	Description       string
	OmitTimeTool      bool
	// ExposeSearchIndexes 控制 search_indexes action 是否对模型可见（默认 false）。
	ExposeSearchIndexes bool
	// HideInternalActions 隐藏内部 action（当前仅 build_system_prompt 已从 enum 移除，
	// 保留该字段以便将来内部 action 扩展）。默认 false。
	HideInternalActions bool
	// UserID 工具的 uid（user 全局合并）。空 = 回退 sessionID（per-session 语义）。
	// 工具构造时从 metadata 读一次缓存到这里（评审 B3：与 ingest 同源）。
	UserID string
}
