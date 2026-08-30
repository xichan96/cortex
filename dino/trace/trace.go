// Package trace implements the context-trace event log: an append-only JSONL
// record of everything that happened during an agent session, separate from the
// chatstore state layer. Design: docs/design/context-trace.md.
//
// The package owns the envelope schema, event payload types, and the async
// Recorder. The Tracer interface that the engine holds lives in agent/hooks
// (agent/engine never imports dino; see design §3.2④). Recording points in the
// engine call Tracer.Record with an Event; the recorder fills the envelope.
package trace

import (
	"encoding/json"

	"github.com/xichan96/cortex/agent/types"
)

// SchemaVersion is the envelope schema version. Bump on semantic changes
// (required-field additions, meaning changes); adding optional fields does not
// bump.
const SchemaVersion = 1

// TraceEvent is the full envelope for one JSONL line (single envelope: partial
// replay is safe — each line is independently parseable).
type TraceEvent struct {
	SchemaVersion  int             `json:"schema_version"`
	Seq            int64           `json:"seq"`                       // monotonic within file, from 1, atomically assigned
	WallTimeUnixMS int64           `json:"wall_time_unix_ms"`         // event enqueue time
	TraceID        string          `json:"trace_id"`                  // one ExecuteStream/Execute call = one turn segment
	SessionID      string          `json:"session_id"`                // file ownership; subagent = parent session + thread suffix
	TurnID         int             `json:"turn_id"`                   // monotonic turn order within session (recorder-assigned)
	Iteration      *int            `json:"iteration,omitempty"`       // engine iteration (llm_call/tool events carry it); nil = not iteration-grained
	ThreadID       string          `json:"thread_id,omitempty"`       // subagent hierarchy path (e.g. "/root/task_1")
	ParentTraceID  string          `json:"parent_trace_id,omitempty"` // subagent provenance (parent turn's TraceID)
	Type           string          `json:"type"`                      // event type (see constants)
	Payload        json.RawMessage `json:"payload"`                   // typed payload (see Payload* structs)
	PayloadRef     string          `json:"payload_ref,omitempty"`     // externalized payload path (default off, §4.4)
}

// Event is the minimal event passed by engine/dino to the recorder; the
// recorder fills the envelope fields.
type Event struct {
	Type          string
	Iteration     *int
	ThreadID      string
	ParentTraceID string
	Payload       any // must be json.Marshal-able
}

// Event type constants. Payload contract documented per constant.

// Event type constants. Payload contract documented per constant.
const (
	// Engine layer (recorded in agent/engine).
	EventTurnStart   = "turn_start"   // Payload: TurnStartPayload
	EventTurnEnd     = "turn_end"     // Payload: TurnEndPayload
	EventLLMCall     = "llm_call"     // Payload: LLMCallPayload
	EventLLMCallEnd  = "llm_call_end" // Payload: LLMCallEndPayload
	EventLLMChunk    = "llm_chunk"    // Payload: ChunkPayload (default off)
	EventToolCall    = "tool_call"    // Payload: ToolCallPayload
	EventToolResult  = "tool_result"  // Payload: ToolResultPayload
	EventToolError   = "tool_error"   // Payload: ToolErrorPayload
	EventCompaction  = "compaction"   // Payload: CompactionPayload
	EventMemorySave  = "memory_save"  // Payload: MemorySavePayload
	EventError       = "error"        // Payload: ErrorPayload

	// dino orchestration layer (session Observer).
	EventOrchestration = "orchestration" // Payload: *session.Event
)

// TurnStartPayload records the beginning of one agent execution.
type TurnStartPayload struct {
	Input           types.AgentInput `json:"input"`
	Model           string           `json:"model"`
	SystemPromptLen int              `json:"system_prompt_len"`
	MaxIterations   int              `json:"max_iterations"`
	ToolNames       []string         `json:"tool_names,omitempty"` // visible tool names (evidence)
}

// TurnEndPayload records the end of one agent execution.
type TurnEndPayload struct {
	Output     string              `json:"output"`
	Usage      types.Usage         `json:"usage"`
	Iterations int                 `json:"iterations"`
	StopCause  types.AgentStopCause `json:"stop_cause,omitempty"`
	WallMS     int64               `json:"wall_ms"` // turn wall clock
}

// LLMCallPayload records one model call start (messages captured at call time).
type LLMCallPayload struct {
	Messages     []types.Message `json:"messages"`      // engine's actual input (volume-controlled, §4.3)
	Tools        []string        `json:"tools,omitempty"`
	EstTokensIn  int             `json:"est_tokens_in"` // types.RoughTokenEstimate
}

// LLMCallEndPayload records one model call end with usage/duration.
type LLMCallEndPayload struct {
	Usage      types.Usage               `json:"usage"`
	DurationMS int64                     `json:"duration_ms"`
	OutputLen  int                       `json:"output_len"`
	Reasoning  string                    `json:"reasoning,omitempty"`
	ToolCalls  []types.ToolCallRequest   `json:"tool_calls,omitempty"`
	HasError   bool                      `json:"has_error,omitempty"`
	Error      string                    `json:"error,omitempty"`
}

// ChunkPayload records merged chunk text (default off, §4.3).
type ChunkPayload struct {
	Content string `json:"content"`
}

// ToolCallPayload records tool execution start.
type ToolCallPayload struct {
	ToolName    string         `json:"tool_name"`
	ToolCallID  string         `json:"tool_call_id"`
	Input       map[string]any `json:"input"`
	StartWallMS int64          `json:"start_wall_ms"`
}

// ToolResultPayload records a successful tool execution.
type ToolResultPayload struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Output     any    `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	Cached     bool   `json:"cached,omitempty"`
}

// ToolErrorPayload records a failed tool execution.
type ToolErrorPayload struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// CompactionPayload records history trimming / summary injection.
type CompactionPayload struct {
	BeforeCount   int    `json:"before_count"`
	AfterCount    int    `json:"after_count"`
	BudgetTokens  int    `json:"budget_tokens"`
	SummaryFolded bool   `json:"summary_folded,omitempty"`
	HasSummary    bool   `json:"has_summary,omitempty"`
	Mode          string `json:"mode"` // "tail_only" | "three_phase" | "none"
}

// MemorySavePayload records a memory save / compression trigger.
type MemorySavePayload struct {
	InputRole         string `json:"input_role"`
	CompressTriggered bool   `json:"compress_triggered"`
	Reason            string `json:"reason,omitempty"` // "threshold" | "compact_after_turns" | ""
}

// ErrorPayload records a stream/iteration error.
type ErrorPayload struct {
	Message   string              `json:"message"`
	StopCause types.AgentStopCause `json:"stop_cause,omitempty"`
}
