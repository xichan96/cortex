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
}
