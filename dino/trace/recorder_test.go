package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/agent/types"
)

func newTestRecorder(t *testing.T, cfg Config) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := NewRecorder(dir, "sess/a", cfg)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func TestEnvelopeFields(t *testing.T) {
	r, _ := newTestRecorder(t, DefaultConfig())
	// Manually push two events through Record and Flush, then read the file.
	r.Record(hooks.TraceEvent{Type: EventTurnStart, Payload: TurnStartPayload{Model: "gpt-4o"}})
	r.Record(hooks.TraceEvent{Type: EventTurnEnd, Payload: TurnEndPayload{Iterations: 1}})
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	lines := readTraceLines(t, r)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}

	var ev0, ev1 TraceEvent
	if err := json.Unmarshal(lines[0], &ev0); err != nil {
		t.Fatalf("line0 unmarshal: %v", err)
	}
	if err := json.Unmarshal(lines[1], &ev1); err != nil {
		t.Fatalf("line1 unmarshal: %v", err)
	}

	if ev0.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", ev0.SchemaVersion, SchemaVersion)
	}
	if ev0.Seq != 1 || ev1.Seq != 2 {
		t.Errorf("seq = %d,%d want 1,2", ev0.Seq, ev1.Seq)
	}
	if ev0.WallTimeUnixMS > ev1.WallTimeUnixMS {
		t.Errorf("wall_time not monotonic: %d > %d", ev0.WallTimeUnixMS, ev1.WallTimeUnixMS)
	}
	if ev0.TraceID == "" || ev1.TraceID == "" {
		t.Errorf("trace_id empty")
	}
	if ev0.SessionID != "sess/a" {
		t.Errorf("session_id = %q, want %q", ev0.SessionID, "sess/a")
	}
	if ev0.TurnID != 1 || ev1.TurnID != 1 {
		t.Errorf("turn_id = %d,%d want 1,1 (same turn)", ev0.TurnID, ev1.TurnID)
	}
}

func TestJSONLAppend(t *testing.T) {
	r, _ := newTestRecorder(t, DefaultConfig())
	for i := 0; i < 5; i++ {
		r.Record(hooks.TraceEvent{Type: EventToolCall, Payload: ToolCallPayload{ToolName: "read_file"}})
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	lines := readTraceLines(t, r)
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %d", len(lines))
	}
	// Row order == enqueue order (seq 1..5).
	for i, l := range lines {
		var ev TraceEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if ev.Seq != int64(i+1) {
			t.Errorf("line %d seq = %d, want %d", i, ev.Seq, i+1)
		}
		if ev.Type != EventToolCall {
			t.Errorf("line %d type = %q", i, ev.Type)
		}
		var p ToolCallPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("line %d payload: %v", i, err)
		}
		if p.ToolName != "read_file" {
			t.Errorf("line %d payload tool_name = %q", i, p.ToolName)
		}
	}
}

func TestFlushDurability(t *testing.T) {
	r, _ := newTestRecorder(t, DefaultConfig())
	r.Record(hooks.TraceEvent{Type: EventTurnStart, Payload: TurnStartPayload{}})
	r.Record(hooks.TraceEvent{Type: EventLLMCall, Payload: LLMCallPayload{}})
	r.Record(hooks.TraceEvent{Type: EventTurnEnd, Payload: TurnEndPayload{}})
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if n := len(readTraceLines(t, r)); n != 3 {
		t.Fatalf("Flush: want 3 readable lines, got %d", n)
	}
}

func TestCrashPartialWrite(t *testing.T) {
	r, dir := newTestRecorder(t, DefaultConfig())
	for i := 0; i < 3; i++ {
		r.Record(hooks.TraceEvent{Type: EventLLMCall, Payload: LLMCallPayload{}})
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// Simulate a crash mid-line: append a truncated line.
	path := filepath.Join(dir, "trace-sess_a.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema_version":1,"seq":4`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	lines := readTraceLines(t, r)
	if len(lines) != 3 {
		t.Fatalf("want 3 valid lines (bad line skipped), got %d", len(lines))
	}
}

func TestDroppedEventsCounter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueSize = 2
	cfg.FlushInterval = time.Hour // keep writer from draining between Record calls
	r, _ := newTestRecorder(t, cfg)

	// Rapidly enqueue far more than the queue size.
	for i := 0; i < 100; i++ {
		r.Record(hooks.TraceEvent{Type: EventLLMCall, Payload: LLMCallPayload{}})
	}
	st := r.Stats()
	// Non-blocking Record: only queue-capacity events land; the rest drop.
	if st.EventsRecorded+st.EventsDropped != 100 {
		t.Errorf("recorded+dropped = %d+%d, want 100", st.EventsRecorded, st.EventsDropped)
	}
	if st.EventsDropped == 0 {
		t.Errorf("events_dropped = 0, want > 0")
	}
	// Close must not hang even with a full queue.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestExternalizePayloadsNotEnabledByDefault(t *testing.T) {
	r, _ := newTestRecorder(t, DefaultConfig())
	r.Record(hooks.TraceEvent{Type: EventToolResult, Payload: ToolResultPayload{ToolName: "bash", Output: "hi"}})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, l := range readTraceLines(t, r) {
		var ev TraceEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.PayloadRef != "" {
			t.Errorf("payload_ref = %q, want empty (externalization default off)", ev.PayloadRef)
		}
	}
}

func TestVolumeControlTruncation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MessageContentMaxLen = 20
	cfg.ToolOutputMaxLen = 20
	r, _ := newTestRecorder(t, cfg)

	long := strings.Repeat("x", 100)
	r.Record(hooks.TraceEvent{Type: EventLLMCall, Payload: LLMCallPayload{Messages: []types.Message{{Content: long}}}})
	r.Record(hooks.TraceEvent{Type: EventToolResult, Payload: ToolResultPayload{ToolName: "bash", Output: long}})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	lines := readTraceLines(t, r)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}

	var llm TraceEvent
	_ = json.Unmarshal(lines[0], &llm)
	var lp LLMCallPayload
	if err := json.Unmarshal(llm.Payload, &lp); err != nil {
		t.Fatal(err)
	}
	if got := lp.Messages[0].Content; len(got) > 40 {
		t.Errorf("message content not truncated: len=%d", len(got))
	}

	var tr TraceEvent
	_ = json.Unmarshal(lines[1], &tr)
	var tp ToolResultPayload
	if err := json.Unmarshal(tr.Payload, &tp); err != nil {
		t.Fatal(err)
	}
	if got, ok := tp.Output.(map[string]any); ok {
		if _, hasOrig := got["original_bytes"]; !hasOrig {
			t.Errorf("truncated tool output missing original_bytes: %v", tp.Output)
		}
	} else {
		t.Errorf("tool output not truncated: %#v", tp.Output)
	}
}

func TestRotate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBytes = 200 // tiny: a few lines rotate
	r, dir := newTestRecorder(t, cfg)

	for i := 0; i < 200; i++ {
		r.Record(hooks.TraceEvent{Type: EventLLMCall, Payload: LLMCallPayload{EstTokensIn: i}})
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	if st := r.Stats(); st.RotationCount == 0 {
		t.Errorf("rotation_count = 0, want > 0")
	}
	if _, err := os.Stat(filepath.Join(dir, "trace-sess_a.1.jsonl")); err != nil {
		t.Errorf("rotated file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trace-sess_a.jsonl")); err != nil {
		t.Errorf("active file missing: %v", err)
	}
}

// readTraceLines scans the session trace file (and .1 if present), skipping
// malformed lines. It reports the raw JSON bytes per valid line.
func readTraceLines(t *testing.T, r *Recorder) [][]byte {
	t.Helper()
	var out [][]byte
	for _, name := range []string{"trace-sess_a.1.jsonl", "trace-sess_a.jsonl"} {
		path := filepath.Join(r.dir, name)
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
				continue // truncated line
			}
			out = append(out, append([]byte(nil), line...))
		}
		f.Close()
	}
	return out
}
