package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

// This file implements the offline replay reducer (design §6): it reads a
// trace JSONL and folds events into either a readable conversation/tool
// sequence (RenderText) or a structured semantic graph (Reduce).

// ReducedTurn is the folded semantic graph for one trace_id (one agent
// execution). Design §6.3.
type ReducedTurn struct {
	TraceID       string             `json:"trace_id"`
	SessionID     string             `json:"session_id"`
	ThreadID      string             `json:"thread_id,omitempty"`
	ParentTraceID string             `json:"parent_trace_id,omitempty"`
	Input         string             `json:"input"`
	FinalOutput   string             `json:"final_output"`
	Iterations    []ReducedIteration `json:"iterations"`
	Usage         types.Usage        `json:"usage"`
	WallMS        int64              `json:"wall_ms"`
	StopCause     string             `json:"stop_cause,omitempty"`
	Error         string             `json:"error,omitempty"`
}

// ReducedIteration groups one engine iteration's LLM calls and tool calls.
type ReducedIteration struct {
	Index     int               `json:"index"`
	LLMCalls  []ReducedLLMCall  `json:"llm_calls"`
	ToolCalls []ReducedToolCall `json:"tool_calls"`
}

// ReducedLLMCall is one model call within an iteration.
type ReducedLLMCall struct {
	Output     string             `json:"output"`
	Reasoning  string             `json:"reasoning,omitempty"`
	Usage      types.Usage        `json:"usage"`
	DurationMS int64              `json:"duration_ms"`
	Tools      []string           `json:"tools,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// ReducedToolCall is one tool execution within an iteration (paired by
// tool_call_id; dangling results are marked).
type ReducedToolCall struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Input      any    `json:"input"`
	Output     any    `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Cached     bool   `json:"cached,omitempty"`
	Dangling   bool   `json:"dangling,omitempty"`
}

// LoadTraces reads all trace-<session>.jsonl (and .1 rotated) files under dir,
// filtering by optional session/trace/thread. Returns events sorted by seq.
func LoadTraces(dir, session, traceID, threadID string) ([]TraceEvent, error) {
	var out []TraceEvent
	pattern := filepath.Join(dir, "trace-*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	// Also include rotated .1 files.
	rotated, err := filepath.Glob(filepath.Join(dir, "trace-*.1.jsonl"))
	if err != nil {
		return nil, err
	}
	matches = append(matches, rotated...)

	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var te TraceEvent
			if err := json.Unmarshal(line, &te); err != nil {
				continue // truncated/corrupt line: skip
			}
			if session != "" && te.SessionID != session {
				continue
			}
			if traceID != "" && te.TraceID != traceID {
				continue
			}
			if threadID != "" && te.ThreadID != threadID {
				continue
			}
			out = append(out, te)
		}
		f.Close()
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// Reduce folds raw events into per-trace ReducedTurns.
func Reduce(events []TraceEvent) []*ReducedTurn {
	// Group by TraceID preserving first-appearance order.
	var order []string
	groups := make(map[string][]TraceEvent)
	for _, ev := range events {
		if _, ok := groups[ev.TraceID]; !ok {
			order = append(order, ev.TraceID)
		}
		groups[ev.TraceID] = append(groups[ev.TraceID], ev)
	}

	turns := make([]*ReducedTurn, 0, len(order))
	for _, traceID := range order {
		turns = append(turns, reduceTrace(groups[traceID]))
	}
	return turns
}

func reduceTrace(evts []TraceEvent) *ReducedTurn {
	turn := &ReducedTurn{}
	var iterIndex int = -1
	var curIter *ReducedIteration
	// tool_call start map: callID -> (input, startIteration)
	type toolStart struct {
		iter *ReducedIteration
		input any
	}
	toolStarts := make(map[string]toolStart)

	for _, ev := range evts {
		turn.TraceID = ev.TraceID
		turn.SessionID = ev.SessionID
		if ev.ThreadID != "" {
			turn.ThreadID = ev.ThreadID
		}
		if ev.ParentTraceID != "" {
			turn.ParentTraceID = ev.ParentTraceID
		}
		if ev.WallTimeUnixMS > 0 && turn.WallMS == 0 {
			// WallMS is filled from turn_end; fallback to first event time below.
		}

		switch ev.Type {
		case EventTurnStart:
			var p TurnStartPayload
			_ = json.Unmarshal(ev.Payload, &p)
			turn.Input = p.Input.Text
			if len(p.Input.Parts) > 0 {
				turn.Input = fmt.Sprintf("%v", p.Input.Parts)
			}
		case EventTurnEnd:
			var p TurnEndPayload
			_ = json.Unmarshal(ev.Payload, &p)
			turn.FinalOutput = p.Output
			turn.Usage = p.Usage
			turn.WallMS = p.WallMS
			turn.StopCause = string(p.StopCause)
		case EventError:
			var p ErrorPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if turn.Error == "" {
				turn.Error = p.Message
			}
		case EventLLMCall:
			if ev.Iteration == nil {
				continue
			}
			if ev.Iteration != nil && *ev.Iteration != iterIndex {
				iterIndex = *ev.Iteration
				turn.Iterations = append(turn.Iterations, ReducedIteration{Index: iterIndex})
				curIter = &turn.Iterations[len(turn.Iterations)-1]
			}
			// llm_call start contributes the tool list for its end pairing.
		case EventLLMCallEnd:
			var p LLMCallEndPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if ev.Iteration != nil && *ev.Iteration != iterIndex {
				iterIndex = *ev.Iteration
				turn.Iterations = append(turn.Iterations, ReducedIteration{Index: iterIndex})
				curIter = &turn.Iterations[len(turn.Iterations)-1]
			}
			if curIter == nil {
				iterIndex = 0
				turn.Iterations = append(turn.Iterations, ReducedIteration{Index: 0})
				curIter = &turn.Iterations[len(turn.Iterations)-1]
			}
			tools := make([]string, 0, len(p.ToolCalls))
			for _, tc := range p.ToolCalls {
				tools = append(tools, tc.Tool)
			}
			curIter.LLMCalls = append(curIter.LLMCalls, ReducedLLMCall{
				Output:     p.Reasoning, // note: output text is not in llm_call_end for stream (in chunks); keep reasoning
				Reasoning:  p.Reasoning,
				Usage:      p.Usage,
				DurationMS: p.DurationMS,
				Tools:      tools,
				Error:      p.Error,
			})
		case EventToolCall:
			var p ToolCallPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if ev.Iteration != nil && *ev.Iteration != iterIndex {
				iterIndex = *ev.Iteration
				turn.Iterations = append(turn.Iterations, ReducedIteration{Index: iterIndex})
				curIter = &turn.Iterations[len(turn.Iterations)-1]
			}
			if curIter == nil {
				iterIndex = 0
				turn.Iterations = append(turn.Iterations, ReducedIteration{Index: 0})
				curIter = &turn.Iterations[len(turn.Iterations)-1]
			}
			toolStarts[p.ToolCallID] = toolStart{iter: curIter, input: p.Input}
		case EventToolResult:
			var p ToolResultPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if st, ok := toolStarts[p.ToolCallID]; ok {
				st.iter.ToolCalls = append(st.iter.ToolCalls, ReducedToolCall{
					ToolName:   p.ToolName,
					ToolCallID: p.ToolCallID,
					Input:      st.input,
					Output:     p.Output,
					DurationMS: p.DurationMS,
					Cached:     p.Cached,
				})
				delete(toolStarts, p.ToolCallID)
			} else {
				turn.Iterations = append(turn.Iterations, ReducedIteration{
					ToolCalls: []ReducedToolCall{{
						ToolName: p.ToolName, ToolCallID: p.ToolCallID, Output: p.Output,
						DurationMS: p.DurationMS, Cached: p.Cached, Dangling: true,
					}},
				})
			}
		case EventToolError:
			var p ToolErrorPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if st, ok := toolStarts[p.ToolCallID]; ok {
				st.iter.ToolCalls = append(st.iter.ToolCalls, ReducedToolCall{
					ToolName: p.ToolName, ToolCallID: p.ToolCallID, Input: st.input,
					Error: p.Error, DurationMS: p.DurationMS,
				})
				delete(toolStarts, p.ToolCallID)
			} else {
				turn.Iterations = append(turn.Iterations, ReducedIteration{
					ToolCalls: []ReducedToolCall{{
						ToolName: p.ToolName, ToolCallID: p.ToolCallID, Error: p.Error,
						DurationMS: p.DurationMS, Dangling: true,
					}},
				})
			}
		}
	}

	// Any unpaired tool_call start => dangling.
	for callID, st := range toolStarts {
		st.iter.ToolCalls = append(st.iter.ToolCalls, ReducedToolCall{
			ToolName: "", ToolCallID: callID, Input: st.input, Dangling: true,
		})
	}

	// Fill WallMS from first/last event timestamps if turn_end missing.
	if turn.WallMS == 0 && len(evts) >= 2 {
		turn.WallMS = evts[len(evts)-1].WallTimeUnixMS - evts[0].WallTimeUnixMS
	}
	return turn
}

// RenderText folds events into a readable conversation/tool sequence (design
// §6.2). It returns the rendered text and a trailing accounting line.
func RenderText(events []TraceEvent) string {
	turns := Reduce(events)
	var sb strings.Builder
	for _, t := range turns {
		sb.WriteString(renderTurn(t))
	}
	return sb.String()
}

func renderTurn(t *ReducedTurn) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "════ Turn %s · session %s", t.TraceID, t.SessionID)
	if t.ThreadID != "" {
		fmt.Fprintf(&sb, " · thread %s", t.ThreadID)
	}
	if t.ParentTraceID != "" {
		fmt.Fprintf(&sb, " · parent %s", t.ParentTraceID)
	}
	fmt.Fprintf(&sb, " · %d iters ════\n", len(t.Iterations))

	prefix := ""
	if t.ThreadID != "" {
		prefix = "  "
	}
	fmt.Fprintf(&sb, "%s→ USER   %s\n", prefix, truncateForRender(t.Input, 120))
	for _, iter := range t.Iterations {
		for _, llm := range iter.LLMCalls {
			fmt.Fprintf(&sb, "%s  · llm_call (iter %d) → %d B · %dms",
				prefix, iter.Index, len(llm.Output), llm.DurationMS)
			if llm.Usage.TotalTokens > 0 {
				fmt.Fprintf(&sb, " · usage p=%d/c=%d/r=%d", llm.Usage.PromptTokens, llm.Usage.CompletionTokens, llm.Usage.ReasoningTokens)
			}
			if len(llm.Tools) > 0 {
				fmt.Fprintf(&sb, " · tools [%s]", strings.Join(llm.Tools, ", "))
			}
			if llm.Error != "" {
				fmt.Fprintf(&sb, " · ERROR %s", truncateForRender(llm.Error, 120))
			}
			sb.WriteString("\n")
		}
		for _, tc := range iter.ToolCalls {
			status := "OK"
			extra := ""
			if tc.Error != "" {
				status = "ERR"
				extra = " · " + truncateForRender(tc.Error, 120)
			}
			dangling := ""
			if tc.Dangling {
				dangling = " · [dangling]"
			}
			fmt.Fprintf(&sb, "%s  └ TOOL %s [%s] → %s · %dms%s%s\n",
				prefix, tc.ToolName, tc.ToolCallID, status, tc.DurationMS, extra, dangling)
		}
	}
	if t.FinalOutput != "" {
		fmt.Fprintf(&sb, "%s  → final: %s\n", prefix, truncateForRender(t.FinalOutput, 160))
	}
	if t.Error != "" {
		fmt.Fprintf(&sb, "%s  → ERROR: %s\n", prefix, truncateForRender(t.Error, 160))
	}
	fmt.Fprintf(&sb, "════ 总计: %d iters · prompt=%d · wall=%dms%s ════\n",
		len(t.Iterations), t.Usage.PromptTokens, t.WallMS, stopCauseSuffix(t.StopCause))
	return sb.String()
}

func stopCauseSuffix(sc string) string {
	if sc == "" {
		return ""
	}
	return " · stop=" + sc
}

func truncateForRender(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// WriteJSONL writes events as raw JSONL (debug output).
func WriteJSONL(w io.Writer, events []TraceEvent) error {
	enc := json.NewEncoder(w)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return nil
}
