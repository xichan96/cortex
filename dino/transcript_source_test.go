package dino

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// transcriptSourceProviderName registers a streaming provider that emits a
// proper "chunk" (the package mock streams no Type, which the engine drops).
const transcriptSourceProviderName = "mock-transcript-source"

func registerTranscriptSourceProvider(t *testing.T, response string) {
	t.Helper()
	RegisterLLMProvider(transcriptSourceProviderName, func(cfg *Config) (types.LLMProvider, error) {
		return &transcriptLiveProvider{response: response}, nil
	})
}

// TestTranscriptSourceOfTruthRoundTrip verifies the P7-A grayscale path end to
// end: a turn appends both messages to the JSONL file, GetChatHistory rebuilds
// them in order, and reopening the same session ID after CloseSession reads the
// history back from disk (not from chatstore).
func TestTranscriptSourceOfTruthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	registerTranscriptSourceProvider(t, "test response")
	cfg := getTestConfig()
	cfg.Provider.Type = transcriptSourceProviderName
	cfg.Transcript.SourceOfTruth = true
	cfg.Transcript.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	defer client.CloseAll()
	ctx := context.Background()

	const sessionID = "transcript-source-roundtrip"
	cs, err := client.CreateSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := cs.SendAndWait(ctx, types.NewAgentInput("source question")); err != nil {
		t.Fatalf("SendAndWait: %v", err)
	}

	path := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source transcript was not created: %v", err)
	}

	want := []types.Message{
		{Role: "user", Content: "source question"},
		{Role: "assistant", Content: "test response"},
	}
	assertHistory(t, cs.GetMemory(), want, "in-memory read")

	// Close the session (flushes + closes the log), then reopen the same ID:
	// the provider must rebuild history from the existing JSONL file.
	factory.CloseSession(sessionID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source transcript disappeared after close: %v", err)
	}

	reopened, err := factory.CreateSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("reopen CreateSession: %v", err)
	}
	defer factory.CloseSession(sessionID)
	assertHistory(t, reopened.GetMemory(), want, "reopened read from JSONL")
}

func assertHistory(t *testing.T, mem types.MemoryProvider, want []types.Message, label string) {
	t.Helper()
	if mem == nil {
		t.Fatalf("%s: session memory is nil", label)
	}
	got, err := mem.GetChatHistory(context.Background())
	if err != nil {
		t.Fatalf("%s: GetChatHistory: %v", label, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %d messages, want %d: %+v", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("%s: message %d = {role:%q content:%q}, want {role:%q content:%q}",
				label, i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
}

// TestTranscriptSourceDisabledUsesChatstore is the regression guard: with the
// flag off, session memory stays on chatstore and no JSONL is opened.
func TestTranscriptSourceDisabledUsesChatstore(t *testing.T) {
	dir := t.TempDir()
	registerTranscriptSourceProvider(t, "test response")
	cfg := getTestConfig()
	cfg.Provider.Type = transcriptSourceProviderName
	cfg.Transcript.SourceOfTruth = false
	cfg.Transcript.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	defer client.CloseAll()
	ctx := context.Background()

	const sessionID = "transcript-source-disabled"
	cs, err := client.CreateSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := cs.SendAndWait(ctx, types.NewAgentInput("chatstore question")); err != nil {
		t.Fatalf("SendAndWait: %v", err)
	}

	assertHistory(t, cs.GetMemory(), []types.Message{
		{Role: "user", Content: "chatstore question"},
		{Role: "assistant", Content: "test response"},
	}, "chatstore read")

	if df, ok := factory.(*dinoFactory); ok {
		if src := df.transcriptSourceFor(sessionID); src != nil {
			t.Fatalf("source transcript should not be registered when disabled: %s", src.Path())
		}
	}
	path := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source transcript should not exist when disabled: %v", err)
	}
}
