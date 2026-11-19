package dino

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/transcript"
	"github.com/xichan96/cortex/agent/types"
)

// transcriptDir resolves where exported JSONL transcripts live. Explicit
// config wins; otherwise the agent-home sessions directory, then the memory
// persistence directory, then the legacy dino_sessions location.
func (f *dinoFactory) transcriptDir() string {
	if dir := strings.TrimSpace(f.config.Transcript.Dir); dir != "" {
		return dir
	}
	if f.home != nil && f.home.paths.SessionsDir != "" {
		return f.home.paths.SessionsDir
	}
	if dir := strings.TrimSpace(f.config.Memory.PersistDirectory); dir != "" {
		return filepath.Join(dir, "transcripts")
	}
	return filepath.Join("dino_sessions", "transcripts")
}

// ExportSessionTranscript writes the session's current chat history as a new
// JSONL transcript and returns its path. The export is a point-in-time copy:
// it reads through the session's MemoryProvider, not the live engine.
//
// The target file is replaced atomically via a temporary file + rename, so a
// concurrent reader never sees a half-written transcript.
func (f *dinoFactory) ExportSessionTranscript(ctx context.Context, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("export transcript: empty session id")
	}
	s := f.GetSession(sessionID)
	if s == nil {
		return "", fmt.Errorf("export transcript: session %q not found", sessionID)
	}
	mem := s.GetMemory()
	if mem == nil {
		return "", fmt.Errorf("export transcript: session %q has no memory provider", sessionID)
	}
	messages, err := mem.GetChatHistory(ctx)
	if err != nil {
		return "", fmt.Errorf("export transcript: read history: %w", err)
	}

	// Source-of-truth (P7-A): the memory provider's file is already canonical;
	// flush it and return the path. Replacing it below (temp + rename) would
	// detach the provider's open descriptor.
	if src := f.transcriptSourceFor(sessionID); src != nil {
		if err := src.Sync(); err != nil {
			return "", fmt.Errorf("export transcript: flush source transcript: %w", err)
		}
		return src.Path(), nil
	}

	// Live dual-write (P5-A): when the session already streams into
	// <dir>/<name>.jsonl, flush that file and return it. Replacing the file
	// below (temp file + rename) would detach the live writer's open
	// descriptor, so its later appends would vanish from the path. The live
	// file is already the canonical copy; export just needs to ensure it is
	// on disk.
	if lt := f.liveTranscriptFor(sessionID); lt != nil {
		if path, ok, err := lt.exportPath(ctx, mem); err != nil {
			return "", fmt.Errorf("export transcript: flush live transcript: %w", err)
		} else if ok {
			return path, nil
		}
	}

	dir := f.transcriptDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("export transcript: create dir %s: %w", dir, err)
	}
	finalPath := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")
	tmp, err := os.CreateTemp(dir, ".transcript-*.jsonl")
	if err != nil {
		return "", fmt.Errorf("export transcript: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)

	log, err := transcript.Open(tmpPath, transcript.OpenOptions{
		Create: true,
		Header: transcript.Header{
			ID:        sessionID,
			CWD:       f.config.WorkspaceRoot,
			Timestamp: time.Now().UTC(),
		},
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + ".lock")
		return "", fmt.Errorf("export transcript: open temp: %w", err)
	}
	defer os.Remove(tmpPath + ".lock")
	for _, msg := range messages {
		if _, err := log.AppendMessage(msg); err != nil {
			_ = log.Close()
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("export transcript: append message: %w", err)
		}
	}
	if err := log.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("export transcript: close temp: %w", err)
	}
	// Rename replaces the destination on Unix; remove first for Windows.
	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("export transcript: publish %s: %w", finalPath, err)
	}
	return finalPath, nil
}

// ImportSessionTranscript rebuilds the model-visible messages from a JSONL
// transcript. It is read-only and does not attach the transcript to a session.
func (f *dinoFactory) ImportSessionTranscript(ctx context.Context, path string) ([]types.Message, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("import transcript: empty path")
	}
	log, err := transcript.Open(path, transcript.OpenOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("import transcript: open %s: %w", path, err)
	}
	defer log.Close()

	messages, _, err := log.BuildContext(ctx, transcript.DefaultBuildOptions())
	if err != nil {
		return nil, fmt.Errorf("import transcript: rebuild context: %w", err)
	}
	return messages, nil
}

func safeTranscriptName(sessionID string) string {
	name := strings.TrimSpace(sessionID)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, ":", "-")
	if name == "" {
		name = "session"
	}
	return name
}
