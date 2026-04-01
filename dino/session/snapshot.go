package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xichan96/cortex/agent/types"
)

const FileSnapshotVersion = 1

// FileSnapshot is a restart/recovery aid. Messages are whatever GetChatHistory returns (windowed LLM view), not a full dump of all rows in persistent storage.
type FileSnapshot struct {
	Version              int             `json:"version"`
	SessionID            string          `json:"session_id"`
	Messages             []types.Message `json:"messages"`
	Usage                types.Usage     `json:"usage"`
	MemoryCompletedTurns uint32          `json:"memory_completed_turns"`
}

func SaveFileSnapshot(path string, snap *FileSnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil snapshot")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func LoadFileSnapshot(path string) (*FileSnapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap FileSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	if snap.Version != FileSnapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d", snap.Version)
	}
	return &snap, nil
}

func BuildSnapshotFromSession(ctx context.Context, s *Session) (*FileSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("nil session")
	}
	snap := &FileSnapshot{
		Version:   FileSnapshotVersion,
		SessionID: s.ID(),
	}
	ag := s.GetAgent()
	if ag != nil {
		snap.Usage = ag.GetTotalUsage()
		snap.MemoryCompletedTurns = ag.MemoryTurnCounter()
	}
	mem := s.GetMemory()
	if mem != nil {
		msgs, err := mem.GetChatHistory(ctx)
		if err != nil {
			return nil, err
		}
		snap.Messages = msgs
	}
	return snap, nil
}

func ApplySnapshotToSession(ctx context.Context, s *Session, snap *FileSnapshot) error {
	if s == nil || snap == nil {
		return fmt.Errorf("nil session or snapshot")
	}
	if snap.SessionID != s.ID() {
		return fmt.Errorf("snapshot session_id %q != session %q", snap.SessionID, s.ID())
	}
	ag := s.GetAgent()
	if ag == nil {
		return fmt.Errorf("no agent")
	}
	mem := s.GetMemory()
	if len(snap.Messages) > 0 {
		if mem == nil {
			return fmt.Errorf("snapshot has messages but session has no memory")
		}
		replay, ok := mem.(types.MemoryReplay)
		if !ok {
			return fmt.Errorf("memory does not implement MemoryReplay")
		}
		if err := replay.ReplayMessages(ctx, snap.Messages); err != nil {
			return err
		}
	}
	ag.RestoreTotalUsage(snap.Usage)
	ag.RestoreMemoryTurnCounter(snap.MemoryCompletedTurns)
	return nil
}
