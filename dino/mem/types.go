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

type MemoryToolOptions struct {
	SessionID         string
	PersistDir        string
	SQLiteFile        string
	Log               *slog.Logger
	ToolName          string
	WriteKnowledgeTag string
	Description       string
	OmitTimeTool      bool
}
