package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/agent/hooks"
	"github.com/xichan96/cortex/pkg/logger"
)

// Config controls the recorder's write behaviour. All fields have safe
// defaults via DefaultConfig.
type Config struct {
	// Dir is the trace output directory. Empty falls back to DefaultConfig.Dir.
	Dir string
	// QueueSize is the events channel capacity. Record drops (non-blocking)
	// once full, counting dropped_events.
	QueueSize int
	// FlushInterval is how often the writer goroutine flushes the bufio writer.
	FlushInterval time.Duration
	// BatchSize flushes when len(events) in the channel >= BatchSize.
	BatchSize int
	// CaptureFullMessages records the full llm_call Messages (default off:
	// each message content is truncated).
	CaptureFullMessages bool
	// CaptureFullToolOutput records the full tool_result Output (default off:
	// output is truncated with count fields).
	CaptureFullToolOutput bool
	// CaptureChunks records llm_chunk events (default off; high volume).
	CaptureChunks bool
	// MaxBytes is the per-file size cap before rotation (default 512MB).
	MaxBytes int64
	// MessageContentMaxLen caps each llm_call message content when
	// CaptureFullMessages is false. 0 = default 4096.
	MessageContentMaxLen int
	// ToolOutputMaxLen caps each tool_result output when
	// CaptureFullToolOutput is false. 0 = default 60000.
	ToolOutputMaxLen int
}

// DefaultConfig returns a Config with the design's default values.
func DefaultConfig() Config {
	return Config{
		Dir:                  "./dino_sessions/traces",
		QueueSize:            256,
		FlushInterval:        200 * time.Millisecond,
		BatchSize:            512,
		CaptureFullMessages:  false,
		CaptureFullToolOutput: false,
		CaptureChunks:        false,
		MaxBytes:             512 << 20, // 512MB
		MessageContentMaxLen: 4096,
		ToolOutputMaxLen:     60000,
	}
}

// Stats exposes observable counters for diagnostics.
type Stats struct {
	// EventsRecorded is the number of events enqueued (never drops).
	EventsRecorded int64
	// EventsDropped counts non-blocking Record drops (channel full).
	EventsDropped int64
	// BytesWritten is the total payload bytes written to files.
	BytesWritten int64
	// RotationCount counts file rotations.
	RotationCount int64
}

// Recorder is a session-bound trace writer. It owns one events channel and one
// writer goroutine; Record is non-blocking (drops + counts when full), Flush
// synchronously drains + fsyncs, Close is idempotent.
//
// Zero value is invalid; construct via NewRecorder. Recorder must not be copied
// after first use.
type Recorder struct {
	dir       string
	sessionID string
	cfg       Config

	fileSafeID string

	traceSeq   atomic.Int64 // seq within the current file
	turnID     atomic.Int64 // turn counter, starts 1
	curTraceID atomic.Value // string; the current turn's trace_id ("" until first turn_start)

	events chan Event
	flush  chan chan error

	mu        sync.Mutex // guards writer state below
	w         *bufio.Writer
	f         *os.File
	fileBytes int64
	closed    bool
	closeOnce sync.Once

	stopCh    chan struct{}
	stopWG    sync.WaitGroup
	writeErrs []error

	stats Stats
}

// NewRecorder creates a session-bound recorder. sessionID may contain '/' — the
// file name escapes it. Errors are returned when the directory cannot be
// created or the file opened; callers should treat failure as "trace disabled"
// (never block the session).
func NewRecorder(dir, sessionID string, cfg Config) (*Recorder, error) {
	if dir == "" {
		dir = DefaultConfig().Dir
	}
	cfg = applyConfigDefaults(cfg)

	r := &Recorder{
		dir:        dir,
		sessionID:  sessionID,
		cfg:        cfg,
		fileSafeID: escapeSessionID(sessionID),
		events:     make(chan Event, cfg.QueueSize),
		flush:      make(chan chan error),
		stopCh:     make(chan struct{}),
	}
	r.turnID.Store(1)
	r.curTraceID.Store("")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("trace: mkdir %s: %w", dir, err)
	}
	if err := r.openFile(); err != nil {
		return nil, err
	}

	r.stopWG.Add(1)
	go r.writerLoop()
	return r, nil
}

func applyConfigDefaults(cfg Config) Config {
	d := DefaultConfig()
	if cfg.Dir == "" {
		cfg.Dir = d.Dir
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = d.QueueSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = d.FlushInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = d.BatchSize
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = d.MaxBytes
	}
	if cfg.MessageContentMaxLen <= 0 {
		cfg.MessageContentMaxLen = d.MessageContentMaxLen
	}
	if cfg.ToolOutputMaxLen <= 0 {
		cfg.ToolOutputMaxLen = d.ToolOutputMaxLen
	}
	return cfg
}

func escapeSessionID(id string) string {
	return strings.ReplaceAll(id, "/", "_")
}

func (r *Recorder) openFile() error {
	path := filepath.Join(r.dir, "trace-"+r.fileSafeID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("trace: open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("trace: stat %s: %w", path, err)
	}
	r.f = f
	r.fileBytes = st.Size()
	r.w = bufio.NewWriterSize(f, 64<<10)
	return nil
}

// Record appends an event, non-blocking. It drops (and counts) when the channel
// is full — trace is a side channel and must never block the engine.
// It accepts hooks.TraceEvent (the engine's minimal event) and converts to the
// package's Event, so Recorder implements hooks.Tracer directly.
func (r *Recorder) Record(ev hooks.TraceEvent) {
	internal := Event{
		Type:          ev.Type,
		Iteration:     ev.Iteration,
		ThreadID:      ev.ThreadID,
		ParentTraceID: ev.ParentTraceID,
		Payload:       ev.Payload,
	}
	select {
	case r.events <- internal:
		atomic.AddInt64(&r.stats.EventsRecorded, 1)
	default:
		atomic.AddInt64(&r.stats.EventsDropped, 1)
	}
}

// Flush synchronously drains the channel and fsyncs the file, guaranteeing a
// completed turn is readable. Called at turn_end.
func (r *Recorder) Flush() error {
	if r.isClosed() {
		return nil
	}
	done := make(chan error, 1)
	select {
	case r.flush <- done:
	case <-r.stopCh:
		return nil
	}
	return <-done
}

func (r *Recorder) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Close drains the channel, flushes, fsyncs, and closes the file. Idempotent.
func (r *Recorder) Close() error {
	var err error
	r.closeOnce.Do(func() {
		close(r.stopCh)
		r.stopWG.Wait()

		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		if r.w != nil {
			err = r.w.Flush()
		}
		if err == nil && r.f != nil {
			err = r.f.Sync()
		}
		if r.f != nil {
			cerr := r.f.Close()
			if err == nil {
				err = cerr
			}
		}
		r.w = nil
		r.f = nil
	})
	return err
}

// Stats returns a snapshot of the recorder's counters.
func (r *Recorder) Stats() Stats {
	return Stats{
		EventsRecorded: atomic.LoadInt64(&r.stats.EventsRecorded),
		EventsDropped:  atomic.LoadInt64(&r.stats.EventsDropped),
		BytesWritten:   atomic.LoadInt64(&r.stats.BytesWritten),
		RotationCount:  atomic.LoadInt64(&r.stats.RotationCount),
	}
}

func (r *Recorder) recordWriteErr(err error) {
	r.writeErrs = append(r.writeErrs, err)
	logger.LogError("trace", err, slog.String("session", r.sessionID))
}

// writerLoop drains events and writes JSONL lines. It is the only goroutine
// touching r.f/r.w. Rotation happens inline on size cap.
func (r *Recorder) writerLoop() {
	defer r.stopWG.Done()

	batch := make([]Event, 0, r.cfg.BatchSize)
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()

	needsFlush := func() bool { return len(batch) >= r.cfg.BatchSize }

	// writeBatch writes the current batch to the file and returns the first
	// error encountered (subsequent errors still attempted).
	writeBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			batch = batch[:0]
			return nil
		}
		var firstErr error
		for _, ev := range batch {
			if err := r.writeEventLocked(ev); err != nil {
				r.recordWriteErr(err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if r.w != nil {
			if err := r.w.Flush(); err != nil {
				r.recordWriteErr(err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		batch = batch[:0]
		return firstErr
	}

	// flushFile drains any remaining queued events then syncs to disk.
	flushFile := func() error {
		// Drain any events that arrived after the last select.
	drain:
		for {
			select {
			case ev, ok := <-r.events:
				if !ok {
					break drain
				}
				batch = append(batch, ev)
			default:
				break drain
			}
		}
		firstErr := writeBatch()
		r.mu.Lock()
		if r.f != nil {
			if err := r.f.Sync(); err != nil {
				r.recordWriteErr(err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		r.mu.Unlock()
		return firstErr
	}

	for {
		select {
		case ev, ok := <-r.events:
			if !ok {
				_ = writeBatch()
				return
			}
			batch = append(batch, ev)
			if needsFlush() {
				_ = writeBatch()
			}
		case done := <-r.flush:
			done <- flushFile()
		case <-ticker.C:
			if len(batch) > 0 {
				_ = writeBatch()
			}
		case <-r.stopCh:
			// Drain remaining channel events before stopping.
			_ = flushFile()
			return
		}
	}
}

// writeEventLocked marshals and appends one envelope. Caller holds r.mu.
func (r *Recorder) writeEventLocked(ev Event) error {
	// A turn_start opens a new trace_id (one ExecuteStream/Execute call = one
	// turn segment); every subsequent event until the next turn_start shares it.
	if ev.Type == EventTurnStart {
		r.curTraceID.Store(newTraceID())
	}
	seq := r.traceSeq.Add(1)

	te := TraceEvent{
		SchemaVersion:  SchemaVersion,
		Seq:            seq,
		WallTimeUnixMS: time.Now().UnixMilli(),
		TraceID:        r.currentTraceID(),
		SessionID:      r.sessionID,
		TurnID:         int(r.turnID.Load()),
		Iteration:      ev.Iteration,
		ThreadID:       ev.ThreadID,
		ParentTraceID:  ev.ParentTraceID,
		Type:           ev.Type,
	}

	// Volume control before marshal.
	te.Payload, _ = json.Marshal(applyVolumeControl(ev, r.cfg))

	// Rotate before writing when at/over the size cap.
	if r.fileBytes >= r.cfg.MaxBytes {
		if err := r.rotateLocked(); err != nil {
			return err
		}
		// Re-read seq? No: seq continues across files for merged replay. Keep monotonic.
	}

	line, err := json.Marshal(te)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := r.w.Write(line); err != nil {
		return err
	}
	r.fileBytes += int64(len(line))
	atomic.AddInt64(&r.stats.BytesWritten, int64(len(line)))
	return nil
}

// rotateLocked closes the current file, renames it to <name>.1.jsonl, and opens
// a fresh one. Caller holds r.mu.
func (r *Recorder) rotateLocked() error {
	if r.w != nil {
		if err := r.w.Flush(); err != nil {
			r.recordWriteErr(err)
		}
	}
	if r.f != nil {
		if err := r.f.Sync(); err != nil {
			r.recordWriteErr(err)
		}
		if err := r.f.Close(); err != nil {
			return err
		}
	}
	oldPath := filepath.Join(r.dir, "trace-"+r.fileSafeID+".jsonl")
	rotatedPath := filepath.Join(r.dir, "trace-"+r.fileSafeID+".1.jsonl")
	if err := os.Rename(oldPath, rotatedPath); err != nil {
		return err
	}
	atomic.AddInt64(&r.stats.RotationCount, 1)
	if err := r.openFile(); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) currentTraceID() string {
	v, _ := r.curTraceID.Load().(string)
	return v
}

// newTraceID generates a fresh trace_id for a turn. Deterministic-enough for a
// log: time + monotonic counter.
var traceIDCounter atomic.Int64

func newTraceID() string {
	return fmt.Sprintf("trace-%d-%d", time.Now().UnixMilli(), traceIDCounter.Add(1))
}

// applyVolumeControl truncates payload fields per config before marshal.
func applyVolumeControl(ev Event, cfg Config) any {
	switch ev.Type {
	case EventLLMCall:
		p, ok := ev.Payload.(LLMCallPayload)
		if !ok {
			return ev.Payload
		}
		if !cfg.CaptureFullMessages {
			for i := range p.Messages {
				p.Messages[i].Content = truncate(p.Messages[i].Content, cfg.MessageContentMaxLen)
			}
		}
		return p
	case EventToolResult:
		p, ok := ev.Payload.(ToolResultPayload)
		if !ok {
			return ev.Payload
		}
		if !cfg.CaptureFullToolOutput {
			p.Output = truncateAny(p.Output, cfg.ToolOutputMaxLen)
		}
		return p
	case EventLLMChunk:
		if !cfg.CaptureChunks {
			return nil // drop payload entirely; caller records nothing when off
		}
		return ev.Payload
	default:
		return ev.Payload
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 0 {
		return ""
	}
	// UTF-8 safe head cut.
	i := maxLen
	for i > 0 && i < len(s) && (s[i]&0xC0) == 0x80 {
		i--
	}
	return s[:i] + "...(truncated)"
}

func truncateAny(v any, maxLen int) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		orig := len(s)
		s = truncate(s, maxLen)
		return map[string]any{"value": s, "original_bytes": orig}
	}
	return v
}
