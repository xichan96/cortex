package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func TestExportImportSessionTranscript(t *testing.T) {
	dir := t.TempDir()
	cfg := getTestConfig()
	cfg.Transcript.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	sess, err := factory.CreateSession(context.Background(), "transcript-export")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mem := sess.GetMemory()
	if mem == nil {
		t.Fatal("session memory is nil")
	}
	replay, ok := mem.(types.MemoryReplay)
	if !ok {
		t.Fatalf("memory %T does not implement MemoryReplay", mem)
	}
	want := []types.Message{
		{Role: "user", Content: "hello transcript"},
		{Role: "assistant", Content: "hello back"},
	}
	if err := replay.ReplayMessages(context.Background(), want); err != nil {
		t.Fatalf("ReplayMessages: %v", err)
	}

	path, err := factory.ExportSessionTranscript(context.Background(), "transcript-export")
	if err != nil {
		t.Fatalf("ExportSessionTranscript: %v", err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("path = %s, want under %s", path, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript file missing: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("temporary lock file should be cleaned up: %v", err)
	}

	got, err := factory.ImportSessionTranscript(context.Background(), path)
	if err != nil {
		t.Fatalf("ImportSessionTranscript: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("imported %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Export is idempotent: a second run replaces the previous file.
	path2, err := factory.ExportSessionTranscript(context.Background(), "transcript-export")
	if err != nil {
		t.Fatalf("second ExportSessionTranscript: %v", err)
	}
	if path2 != path {
		t.Fatalf("path changed: %s -> %s", path, path2)
	}
}

func TestExportSessionTranscript_DefaultsAndErrors(t *testing.T) {
	cfg := getTestConfig()
	cfg.Transcript.Dir = ""
	cfg.Memory.PersistDirectory = filepath.Join(t.TempDir(), "sessions")

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.ExportSessionTranscript(context.Background(), "missing"); err == nil {
		t.Fatal("export of a missing session must fail")
	}
	if _, err := factory.ImportSessionTranscript(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("import of a missing file must fail")
	}

	if _, err := factory.CreateSession(context.Background(), "transcript-default-dir"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	path, err := factory.ExportSessionTranscript(context.Background(), "transcript-default-dir")
	if err != nil {
		t.Fatalf("ExportSessionTranscript: %v", err)
	}
	wantDir := filepath.Join(cfg.Memory.PersistDirectory, "transcripts")
	if !strings.HasPrefix(path, wantDir) {
		t.Fatalf("default path = %s, want under %s", path, wantDir)
	}
}
