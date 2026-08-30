package engine

import (
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/types"
)

// Trace event type constants (engine side). The recorder maps these to its own
// payload types by name; keeping them here avoids an agent/engine -> dino/trace
// import (design §3.2④).
const (
	traceTurnStart   = "turn_start"
	traceTurnEnd     = "turn_end"
	traceLLMCall     = "llm_call"
	traceLLMCallEnd  = "llm_call_end"
	traceToolCall    = "tool_call"
	traceToolResult  = "tool_result"
	traceToolError   = "tool_error"
	traceCompaction  = "compaction"
	traceMemorySave  = "memory_save"
	traceError       = "error"
)

// recordTrace emits an event when a tracer is attached; no-op otherwise
// (nil-guard zero overhead).
func (ae *AgentEngine) recordTrace(ev hooks.TraceEvent) {
	if t := ae.getTracer(); t != nil {
		t.Record(ev)
	}
}

func traceIterationPtr(iteration int) *int {
	i := iteration
	return &i
}

// traceTurnStartPayload mirrors dino/trace.TurnStartPayload.
type traceTurnStartPayload struct {
	Input           types.AgentInput `json:"input"`
	Model           string           `json:"model"`
	SystemPromptLen int              `json:"system_prompt_len"`
	MaxIterations   int              `json:"max_iterations"`
	ToolNames       []string         `json:"tool_names,omitempty"`
}

// traceTurnEndPayload mirrors dino/trace.TurnEndPayload.
type traceTurnEndPayload struct {
	Output     string               `json:"output"`
	Usage      types.Usage          `json:"usage"`
	Iterations int                  `json:"iterations"`
	StopCause  types.AgentStopCause `json:"stop_cause,omitempty"`
	WallMS     int64                `json:"wall_ms"`
}

// traceLLMCallPayload mirrors dino/trace.LLMCallPayload.
type traceLLMCallPayload struct {
	Messages    []types.Message `json:"messages"`
	Tools       []string        `json:"tools,omitempty"`
	EstTokensIn int             `json:"est_tokens_in"`
}

// traceLLMCallEndPayload mirrors dino/trace.LLMCallEndPayload.
type traceLLMCallEndPayload struct {
	Usage      types.Usage             `json:"usage"`
	DurationMS int64                   `json:"duration_ms"`
	OutputLen  int                     `json:"output_len"`
	Reasoning  string                  `json:"reasoning,omitempty"`
	ToolCalls  []types.ToolCallRequest `json:"tool_calls,omitempty"`
	HasError   bool                    `json:"has_error,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

// traceToolCallPayload mirrors dino/trace.ToolCallPayload.
type traceToolCallPayload struct {
	ToolName    string         `json:"tool_name"`
	ToolCallID  string         `json:"tool_call_id"`
	Input       map[string]any `json:"input"`
	StartWallMS int64          `json:"start_wall_ms"`
}

// traceToolResultPayload mirrors dino/trace.ToolResultPayload.
type traceToolResultPayload struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Output     any    `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	Cached     bool   `json:"cached,omitempty"`
}

// traceToolErrorPayload mirrors dino/trace.ToolErrorPayload.
type traceToolErrorPayload struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// traceCompactionPayload mirrors dino/trace.CompactionPayload.
type traceCompactionPayload struct {
	BeforeCount   int    `json:"before_count"`
	AfterCount    int    `json:"after_count"`
	BudgetTokens  int    `json:"budget_tokens"`
	SummaryFolded bool   `json:"summary_folded,omitempty"`
	HasSummary    bool   `json:"has_summary,omitempty"`
	Mode          string `json:"mode"`
}

// traceMemorySavePayload mirrors dino/trace.MemorySavePayload.
type traceMemorySavePayload struct {
	InputRole         string `json:"input_role"`
	CompressTriggered bool   `json:"compress_triggered"`
	Reason            string `json:"reason,omitempty"`
}

// traceErrorPayload mirrors dino/trace.ErrorPayload.
type traceErrorPayload struct {
	Message   string               `json:"message"`
	StopCause types.AgentStopCause `json:"stop_cause,omitempty"`
}

// nowMillis is a tiny indirection so tests can stub wall-clock.
var nowMillis = func() int64 { return time.Now().UnixMilli() }
