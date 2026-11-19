package dino

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/transcript"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/session"
	"github.com/xichan96/cortex/pkg/logger"
)

// liveTranscript continuously mirrors a session's MemoryProvider chat history
// into the session's JSONL transcript file.
//
// The writer is incremental: it remembers how many history messages were
// already appended and only writes the tail on each sync. The transcript is a
// trusted copy of what the model saw, not a replacement for the memory store.
//
// Locking: the factory keeps its per-session live writers in
// liveTranscripts under transcriptMu. transcriptMu is a leaf lock (no code
// path may acquire f.mu while holding it), so CreateSession/CloseSession —
// which hold f.mu — can safely start/stop writers without deadlocking. The
// writer's own mu serializes syncs; session observer callbacks can fire from
// concurrent turns, and without it two Done events could interleave appends.
type liveTranscript struct {
	mu sync.Mutex

	log      *transcript.Log
	path     string
	appended int
	// last is the last message handed to the log. It detects a history window
	// that rolled forward (same length, different content) without shrinking.
	last types.Message
}

// startLiveTranscript opens (or reopens) the JSONL transcript for sessionID and
// subscribes to the session's events. Failures are logged and degrade to "no
// live transcript": a broken transcript must never block a session.
//
// Caller must hold f.mu (CreateSession does); this function only takes
// transcriptMu, never f.mu.
func (f *dinoFactory) startLiveTranscript(sess *session.Session, sessionID string) {
	if f == nil || sess == nil || !f.config.Transcript.Enabled {
		return
	}

	dir := f.transcriptDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("[DinoFactory] live transcript: create dir",
			slog.String("session_id", sessionID),
			slog.String("dir", dir),
			slog.String("error", err.Error()))
		return
	}
	path := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")

	// Create: an existing file is appended to (Open validates the header and
	// repairs a torn tail). A stale file from a previous process is kept;
	// history is mirrored from appended=0, which can leave older entries
	// ahead of the new batch — see syncLocked for the duplicate risk.
	log, err := transcript.Open(path, transcript.OpenOptions{
		Create: true,
		Header: transcript.Header{
			ID:        sessionID,
			CWD:       f.config.WorkspaceRoot,
			Timestamp: time.Now().UTC(),
		},
	})
	if err != nil {
		logger.Warn("[DinoFactory] live transcript: open",
			slog.String("session_id", sessionID),
			slog.String("path", path),
			slog.String("error", err.Error()))
		return
	}

	lt := &liveTranscript{log: log, path: path}
	mem := sess.GetMemory()

	subID := sess.Subscribe(session.ObserverFunc(func(ev *session.Event) {
		if ev == nil {
			return
		}
		// Done closes a turn (memory was already written by the engine).
		// Error is synced too so an aborted turn still records its user
		// message; Message/ToolResult are not used because Message fires for
		// every streamed chunk.
		if ev.IsDone() || ev.IsError() {
			lt.sync(context.Background(), mem)
		}
	}))

	f.transcriptMu.Lock()
	if f.liveTranscripts == nil {
		f.liveTranscripts = make(map[string]*liveTranscript)
	}
	if _, exists := f.liveTranscripts[sessionID]; exists {
		// Defensive: CreateSession returns early for existing sessions, so a
		// duplicate should not happen. Close the new writer instead of leaking
		// it.
		f.transcriptMu.Unlock()
		sess.Unsubscribe(subID)
		_ = log.Close()
		logger.Warn("[DinoFactory] live transcript: duplicate writer",
			slog.String("session_id", sessionID))
		return
	}
	f.liveTranscripts[sessionID] = lt
	f.transcriptMu.Unlock()

	logger.Info("[DinoFactory] live transcript enabled",
		slog.String("session_id", sessionID),
		slog.String("path", path))
}

// stopLiveTranscript flushes and closes the live writer for sessionID and
// removes it from the registry. Safe to call when no writer exists.
//
// Caller may hold f.mu (CloseSession does); this function only takes
// transcriptMu.
func (f *dinoFactory) stopLiveTranscript(sessionID string, sess *session.Session) {
	if f == nil {
		return
	}
	f.transcriptMu.Lock()
	lt := f.liveTranscripts[sessionID]
	delete(f.liveTranscripts, sessionID)
	f.transcriptMu.Unlock()
	if lt == nil {
		return
	}

	var mem types.MemoryProvider
	if sess != nil {
		mem = sess.GetMemory()
	}
	if err := lt.close(context.Background(), mem); err != nil {
		logger.Warn("[DinoFactory] live transcript: close",
			slog.String("session_id", sessionID),
			slog.String("path", lt.path),
			slog.String("error", err.Error()))
	}
}

// stopAllLiveTranscripts flushes and closes every live writer. Used by
// CloseAll and Shutdown.
func (f *dinoFactory) stopAllLiveTranscripts() {
	if f == nil {
		return
	}
	f.transcriptMu.Lock()
	writers := f.liveTranscripts
	f.liveTranscripts = make(map[string]*liveTranscript)
	f.transcriptMu.Unlock()

	for sid, lt := range writers {
		if lt == nil {
			continue
		}
		if err := lt.close(context.Background(), nil); err != nil {
			logger.Warn("[DinoFactory] live transcript: close",
				slog.String("session_id", sid),
				slog.String("path", lt.path),
				slog.String("error", err.Error()))
		}
	}
}

// liveTranscriptFor returns the active live writer for sessionID, or nil.
func (f *dinoFactory) liveTranscriptFor(sessionID string) *liveTranscript {
	if f == nil {
		return nil
	}
	f.transcriptMu.Lock()
	defer f.transcriptMu.Unlock()
	return f.liveTranscripts[sessionID]
}

// sync mirrors the current chat history into the JSONL file.
func (lt *liveTranscript) sync(ctx context.Context, mem types.MemoryProvider) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.syncLocked(ctx, mem)
}

// syncLocked is sync with lt.mu held.
//
// Window handling: GetChatHistory returns a windowed view (MaxHistoryMessages
// + optional summary), so the history can shrink or roll forward when memory
// is compressed. We detect both by length and by the last-appended message.
// On a rollback we restart from index 0 and rewrite the visible window, which
// may duplicate messages that were appended before the rollback (the JSONL is
// append-only by design; duplicates are preferable to silently dropping the
// current context). A genuine append failure only logs a warning and leaves
// `appended` where it was, so the next Done retries from the same point.
func (lt *liveTranscript) syncLocked(ctx context.Context, mem types.MemoryProvider) {
	if lt.log == nil || mem == nil {
		return
	}
	history, err := mem.GetChatHistory(ctx)
	if err != nil {
		logger.Warn("[DinoFactory] live transcript: read history",
			slog.String("path", lt.path),
			slog.String("error", err.Error()))
		return
	}
	if lt.appended > len(history) || (lt.appended > 0 && !messagesEqual(history[lt.appended-1], lt.last)) {
		lt.appended = 0
	}
	for _, msg := range history[lt.appended:] {
		if _, err := lt.log.AppendMessage(msg); err != nil {
			logger.Warn("[DinoFactory] live transcript: append",
				slog.String("path", lt.path),
				slog.String("error", err.Error()))
			return
		}
		lt.appended++
		lt.last = msg
	}
}

// close syncs, fsyncs and closes the log. Subsequent syncs are no-ops.
func (lt *liveTranscript) close(ctx context.Context, mem types.MemoryProvider) error {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.log == nil {
		return nil
	}
	lt.syncLocked(ctx, mem)
	syncErr := lt.log.Sync()
	closeErr := lt.log.Close()
	lt.log = nil
	return errors.Join(syncErr, closeErr)
}

// exportPath syncs the live file and returns its path. ok is false when the
// writer is already closed or no history is available, in which case the
// caller falls back to the regular point-in-time export.
//
// ExportSessionTranscript must not replace the live file (its atomic rename
// would orphan the writer's open descriptor), so a live session is exported
// by flushing the existing file instead.
func (lt *liveTranscript) exportPath(ctx context.Context, mem types.MemoryProvider) (string, bool, error) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.log == nil {
		return "", false, nil
	}
	lt.syncLocked(ctx, mem)
	if err := lt.log.Sync(); err != nil {
		return "", false, err
	}
	return lt.path, true, nil
}

// messagesEqual reports whether two history snapshots represent the same
// message. Used only for window-rollover detection.
func messagesEqual(a, b types.Message) bool {
	return a.Role == b.Role &&
		a.Content == b.Content &&
		a.Name == b.Name &&
		a.ToolCallID == b.ToolCallID &&
		reflect.DeepEqual(a.ToolCalls, b.ToolCalls) &&
		reflect.DeepEqual(a.Parts, b.Parts)
}
