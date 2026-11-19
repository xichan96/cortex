package dino

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/transcript"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

// transcriptMemoryProvider is the P7-A "transcript as source of truth"
// memory provider. Unlike the live dual-write mirror (transcript_live.go),
// this provider is the session's only memory: every SaveContext appends to
// the JSONL log and every GetChatHistory rebuilds the model-visible context
// from it. Nothing is mirrored from chatstore, so there is exactly one writer
// and one source.
//
// The provider also implements AddMessage / ReplayMessages so dino's
// memProvSaver and types.MemoryReplay paths can write through it without
// routing back through chatstore.
//
// Locking: p.mu serializes log replacement (Clear) with appends and reads.
// The transcript.Log has its own mutex; p.mu is always taken first.
type transcriptMemoryProvider struct {
	mu sync.Mutex

	path string
	// header is the session header used when Clear recreates the file. Keeping
	// the original ID keeps the on-disk identity stable across a replay.
	header transcript.Header
	// maxHistory windows GetChatHistory like chatstore's MaxHistoryMessages.
	// <= 0 returns the full branch.
	maxHistory int

	log *transcript.Log
}

// newTranscriptMemoryProvider opens (or creates) <dir>/<safe session id>.jsonl
// and returns a provider backed by it. A corrupt/unsupported existing file is
// returned as an error instead of being silently rewritten.
func newTranscriptMemoryProvider(dir, sessionID, cwd string, maxHistory int) (*transcriptMemoryProvider, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create transcript dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")
	header := transcript.Header{
		ID:        sessionID,
		CWD:       cwd,
		Timestamp: time.Now().UTC(),
	}
	log, err := transcript.Open(path, transcript.OpenOptions{
		Create: true,
		Header: header,
	})
	if err != nil {
		return nil, fmt.Errorf("open transcript %s: %w", path, err)
	}
	return &transcriptMemoryProvider{
		path:       path,
		header:     log.Header(),
		maxHistory: maxHistory,
		log:        log,
	}, nil
}

// LoadMemoryVariables implements types.MemoryProvider. Transcript memory has
// no key/value variables.
func (p *transcriptMemoryProvider) LoadMemoryVariables(context.Context) (map[string]interface{}, error) {
	return nil, nil
}

// SaveContext implements types.MemoryProvider by reusing dino's memoryAdapter
// conversion (input/output map -> messages) with this provider as the write
// target. That keeps the serialized shape identical across both backends.
func (p *transcriptMemoryProvider) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
	return (&memoryAdapter{direct: p}).SaveContext(ctx, input, output)
}

// Clear implements types.MemoryProvider as "start a new log": the append-only
// file cannot express a deletion, so the current file is closed and recreated
// with the same path/header. ReplayMessages is the only caller that needs
// replace semantics (snapshot restore); regular turns never call Clear.
func (p *transcriptMemoryProvider) Clear(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return fmt.Errorf("transcript memory: log is closed")
	}
	closeErr := p.log.Close()
	p.log = nil
	// Remove after Close released the flock; the ".lock" file stays and is
	// reused by the reopened log.
	removeErr := os.Remove(p.path)
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return errors.Join(closeErr, fmt.Errorf("reset transcript %s: %w", p.path, removeErr))
	}
	log, err := transcript.Open(p.path, transcript.OpenOptions{
		Create: true,
		Header: p.header,
	})
	if err != nil {
		return errors.Join(closeErr, fmt.Errorf("reopen transcript %s: %w", p.path, err))
	}
	p.log = log
	return closeErr
}

// GetChatHistory implements types.MemoryProvider from the active transcript
// branch. maxHistory mirrors chatstore's windowing contract so prompts do not
// grow without bound; compaction itself is not performed here (see
// CompressMemory).
func (p *transcriptMemoryProvider) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return nil, fmt.Errorf("transcript memory: log is closed")
	}
	messages, _, err := p.log.BuildContext(ctx, transcript.DefaultBuildOptions())
	if err != nil {
		return nil, err
	}
	if p.maxHistory > 0 && len(messages) > p.maxHistory {
		messages = messages[len(messages)-p.maxHistory:]
	}
	return messages, nil
}

// CompressMemory is a no-op for now (P7-A grayscale). The design's compaction
// step appends a CompactionEntry and rebuilds from its firstKeptEntryId; that
// lands in a later phase. Until then the log is append-only and
// GetChatHistory windows by MaxHistoryMessages, so a no-op is safe and the
// engine's own token-budget trimming still applies.
func (p *transcriptMemoryProvider) CompressMemory(context.Context, types.LLMProvider, int) error {
	return nil
}

// StoredMessageCount reports the number of model-visible messages on the
// active branch. The engine uses it for compression gating.
func (p *transcriptMemoryProvider) StoredMessageCount(ctx context.Context) (int, error) {
	messages, err := p.GetChatHistory(ctx)
	if err != nil {
		return 0, err
	}
	return len(messages), nil
}

// AddMessage appends one message to the transcript. Used by memProvSaver (via
// type assertion) and by ReplayMessages.
func (p *transcriptMemoryProvider) AddMessage(ctx context.Context, msg types.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.log == nil {
		return fmt.Errorf("transcript memory: log is closed")
	}
	_, err := p.log.AppendMessage(msg)
	return err
}

// ReplayMessages implements types.MemoryReplay by resetting the log and
// appending the supplied messages in order.
func (p *transcriptMemoryProvider) ReplayMessages(ctx context.Context, messages []types.Message) error {
	if err := p.Clear(ctx); err != nil {
		return err
	}
	for _, msg := range messages {
		if err := p.AddMessage(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

// Sync flushes the underlying log to disk.
func (p *transcriptMemoryProvider) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return nil
	}
	return p.log.Sync()
}

// Path returns the transcript file path.
func (p *transcriptMemoryProvider) Path() string { return p.path }

// Close flushes and closes the log. Safe to call more than once.
func (p *transcriptMemoryProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return nil
	}
	err := p.log.Close()
	p.log = nil
	return err
}

// registerTranscriptSource records the session's source-of-truth provider.
// transcriptMu is a leaf lock (see liveTranscripts); callers may hold f.mu.
func (f *dinoFactory) registerTranscriptSource(sessionID string, src *transcriptMemoryProvider) error {
	f.transcriptMu.Lock()
	defer f.transcriptMu.Unlock()
	if f.transcriptSources == nil {
		f.transcriptSources = make(map[string]*transcriptMemoryProvider)
	}
	if _, exists := f.transcriptSources[sessionID]; exists {
		return fmt.Errorf("transcript source: session %q already registered", sessionID)
	}
	f.transcriptSources[sessionID] = src
	return nil
}

// transcriptSourceFor returns the session's source-of-truth provider, or nil.
func (f *dinoFactory) transcriptSourceFor(sessionID string) *transcriptMemoryProvider {
	if f == nil {
		return nil
	}
	f.transcriptMu.Lock()
	defer f.transcriptMu.Unlock()
	return f.transcriptSources[sessionID]
}

// stopTranscriptSource syncs, closes and unregisters the session's
// source-of-truth transcript. Safe when no provider is registered.
func (f *dinoFactory) stopTranscriptSource(sessionID string) {
	if f == nil {
		return
	}
	f.transcriptMu.Lock()
	src := f.transcriptSources[sessionID]
	delete(f.transcriptSources, sessionID)
	f.transcriptMu.Unlock()
	if src == nil {
		return
	}
	if err := src.Close(); err != nil {
		logger.Warn("[DinoFactory] transcript source: close",
			slog.String("session_id", sessionID),
			slog.String("path", src.Path()),
			slog.String("error", err.Error()))
	}
}

// stopAllTranscriptSources closes every source-of-truth transcript. Used by
// CloseAll and Shutdown.
func (f *dinoFactory) stopAllTranscriptSources() {
	if f == nil {
		return
	}
	f.transcriptMu.Lock()
	sources := f.transcriptSources
	f.transcriptSources = make(map[string]*transcriptMemoryProvider)
	f.transcriptMu.Unlock()
	for sid, src := range sources {
		if src == nil {
			continue
		}
		if err := src.Close(); err != nil {
			logger.Warn("[DinoFactory] transcript source: close",
				slog.String("session_id", sid),
				slog.String("path", src.Path()),
				slog.String("error", err.Error()))
		}
	}
}
