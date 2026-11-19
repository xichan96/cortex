package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/transcript"
	"github.com/xichan96/cortex/agent/types"
)

// The package-level mock streams a bare StreamMessage without Type, so the
// engine (which only accumulates Type=="chunk") yields an empty assistant
// message. transcriptLiveProvider emits a proper chunk so the test can assert
// the assistant text actually reaches the transcript. Registration happens in
// the test (not init) because other package tests clear the global registry.
const transcriptLiveProviderName = "mock-transcript-live"

type transcriptLiveProvider struct {
	response string
}

func (p *transcriptLiveProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: p.response, Usage: types.Usage{TotalTokens: 30}}, nil
}

func (p *transcriptLiveProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return p.stream(), nil
}

func (p *transcriptLiveProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{Content: p.response, Usage: types.Usage{TotalTokens: 30}}, nil
}

func (p *transcriptLiveProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	return p.stream(), nil
}

func (p *transcriptLiveProvider) stream() chan types.StreamMessage {
	ch := make(chan types.StreamMessage, 2)
	ch <- types.StreamMessage{Type: "chunk", Content: p.response}
	ch <- types.StreamMessage{Usage: &types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}}
	close(ch)
	return ch
}

func (p *transcriptLiveProvider) GetModelName() string { return "mock-transcript-live" }

func (p *transcriptLiveProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "mock-transcript-live", MaxTokens: 4096}
}

// readLiveTranscript returns the message entries currently on disk. The live
// writer holds the flock, but transcript readers are allowed concurrently.
func readLiveTranscript(t *testing.T, path string) []*transcript.MessageEntry {
	t.Helper()
	log, err := transcript.Open(path, transcript.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open transcript %s: %v", path, err)
	}
	defer log.Close()
	if diags := log.Diagnostics(); len(diags) > 0 {
		t.Fatalf("transcript has diagnostics: %v", diags)
	}
	var msgs []*transcript.MessageEntry
	for _, entry := range log.Entries() {
		if m, ok := entry.(*transcript.MessageEntry); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

func transcriptHasMessage(entries []*transcript.MessageEntry, role, content string) bool {
	for _, e := range entries {
		if e.Message.Role == role && strings.Contains(e.Message.Content, content) {
			return true
		}
	}
	return false
}

func TestTranscriptLiveDualWrite(t *testing.T) {
	dir := t.TempDir()
	RegisterLLMProvider(transcriptLiveProviderName, func(cfg *Config) (types.LLMProvider, error) {
		return &transcriptLiveProvider{response: "live response"}, nil
	})

	cfg := getTestConfig()
	cfg.Provider.Type = transcriptLiveProviderName
	cfg.Transcript.Enabled = true
	cfg.Transcript.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	defer client.CloseAll()
	ctx := context.Background()

	const sessionID = "transcript-live"
	cs, err := client.CreateSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	path := filepath.Join(dir, safeTranscriptName(sessionID)+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live transcript was not created at session start: %v", err)
	}

	// Round 1: SendAndWait returns on EventTypeDone, which is emitted after
	// the live observer synced the turn's messages.
	if _, err := cs.SendAndWait(ctx, types.NewAgentInput("first question")); err != nil {
		t.Fatalf("SendAndWait round 1: %v", err)
	}
	afterFirst := readLiveTranscript(t, path)
	dump := func(entries []*transcript.MessageEntry) string {
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(e.Message.Role + "=" + e.Message.Content + "; ")
		}
		return b.String()
	}
	if !transcriptHasMessage(afterFirst, "user", "first question") {
		t.Fatalf("user message missing from live transcript: %s", dump(afterFirst))
	}
	if !transcriptHasMessage(afterFirst, "assistant", "live response") {
		t.Fatalf("assistant message missing from live transcript: %s", dump(afterFirst))
	}
	firstCount := len(afterFirst)

	// Round 2: only the new tail is appended; earlier entries are preserved.
	if _, err := cs.SendAndWait(ctx, types.NewAgentInput("second question")); err != nil {
		t.Fatalf("SendAndWait round 2: %v", err)
	}
	afterSecond := readLiveTranscript(t, path)
	if len(afterSecond) <= firstCount {
		t.Fatalf("second round did not append: %d -> %d entries", firstCount, len(afterSecond))
	}
	if !transcriptHasMessage(afterSecond, "user", "first question") {
		t.Fatalf("earlier entries were lost: %+v", afterSecond)
	}
	if !transcriptHasMessage(afterSecond, "user", "second question") {
		t.Fatalf("second user message missing: %+v", afterSecond)
	}

	// Export still works while live: it flushes and returns the live file
	// (replacing it would orphan the writer's descriptor).
	exported, err := factory.ExportSessionTranscript(ctx, sessionID)
	if err != nil {
		t.Fatalf("ExportSessionTranscript: %v", err)
	}
	if exported != path {
		t.Fatalf("exported path = %s, want live path %s", exported, path)
	}
	exportedMsgs := readLiveTranscript(t, exported)
	if len(exportedMsgs) != len(afterSecond) {
		t.Fatalf("exported transcript shows %d messages, want %d", len(exportedMsgs), len(afterSecond))
	}

	// CloseSession flushes, fsyncs and releases the writer lock; the file
	// must still be readable and importable.
	factory.CloseSession(sessionID)
	closed := readLiveTranscript(t, path)
	if len(closed) != len(afterSecond) {
		t.Fatalf("transcript changed on close: %d -> %d messages", len(afterSecond), len(closed))
	}
	imported, err := factory.ImportSessionTranscript(ctx, path)
	if err != nil {
		t.Fatalf("ImportSessionTranscript after close: %v", err)
	}
	if len(imported) != len(closed) {
		t.Fatalf("imported %d messages, want %d", len(imported), len(closed))
	}
}

// fakeHistoryMemory lets the test drive GetChatHistory directly.
type fakeHistoryMemory struct {
	mu   sync.Mutex
	msgs []types.Message
}

func (f *fakeHistoryMemory) LoadMemoryVariables(context.Context) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeHistoryMemory) SaveContext(context.Context, map[string]interface{}, map[string]interface{}) error {
	return nil
}
func (f *fakeHistoryMemory) Clear(context.Context) error { return nil }
func (f *fakeHistoryMemory) GetChatHistory(context.Context) ([]types.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.Message, len(f.msgs))
	copy(out, f.msgs)
	return out, nil
}
func (f *fakeHistoryMemory) CompressMemory(context.Context, types.LLMProvider, int) error {
	return nil
}
func (f *fakeHistoryMemory) set(msgs ...types.Message) {
	f.mu.Lock()
	f.msgs = msgs
	f.mu.Unlock()
}

func TestTranscriptLiveWindowRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.jsonl")
	log, err := transcript.Open(path, transcript.OpenOptions{
		Create: true,
		Header: transcript.Header{ID: "window"},
	})
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	lt := &liveTranscript{log: log, path: path}
	defer func() {
		if err := lt.close(context.Background(), nil); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	mem := &fakeHistoryMemory{}
	ctx := context.Background()

	first := []types.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	mem.set(first...)
	lt.sync(ctx, mem)
	if got := lt.log.Len(); got != 2 {
		t.Fatalf("after first sync: %d entries, want 2", got)
	}

	// A repeated Done event for unchanged history must not duplicate entries.
	lt.sync(ctx, mem)
	if got := lt.log.Len(); got != 2 {
		t.Fatalf("repeated sync duplicated entries: %d, want 2", got)
	}

	// The window shrank (compression): restart from zero. The old entries stay
	// on disk (append-only) and the visible window is rewritten.
	mem.set(types.Message{Role: "assistant", Content: "b"})
	lt.sync(ctx, mem)
	if got := lt.log.Len(); got != 3 {
		t.Fatalf("after shrink: %d entries, want 3", got)
	}
	if lt.appended != 1 {
		t.Fatalf("appended = %d, want 1 after shrink", lt.appended)
	}

	// Same-length but rolled window: detected via the last-appended snapshot.
	mem.set(
		types.Message{Role: "user", Content: "c"},
		types.Message{Role: "assistant", Content: "d"},
	)
	lt.sync(ctx, mem)
	if got := lt.log.Len(); got != 5 {
		t.Fatalf("after roll: %d entries, want 5", got)
	}
	if lt.appended != 2 {
		t.Fatalf("appended = %d, want 2", lt.appended)
	}

	// Reading the file back yields the full append-only history.
	msgs := readLiveTranscript(t, path)
	if len(msgs) != 5 {
		t.Fatalf("persisted %d message entries, want 5", len(msgs))
	}
	if msgs[len(msgs)-1].Message.Content != "d" {
		t.Fatalf("last persisted message = %q, want d", msgs[len(msgs)-1].Message.Content)
	}
}
