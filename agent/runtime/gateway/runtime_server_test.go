package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/runtime"
)

type fakeRuntime struct {
	mu         sync.Mutex
	prompts    []runtime.PromptInput
	steers     []runtime.Message
	followUps  []runtime.Message
	aborted    bool
	subs       map[int]func(runtime.Event)
	nextSub    int
	subscribed chan struct{}
	settled    chan struct{}
	state      runtime.State
	entries    *runtime.EntriesResult
}

func newFakeRuntime() *fakeRuntime {
	settled := make(chan struct{})
	close(settled)
	return &fakeRuntime{
		subs:       make(map[int]func(runtime.Event)),
		subscribed: make(chan struct{}),
		settled:    settled,
		state:      runtime.State{SessionID: "fake-session"},
		entries:    &runtime.EntriesResult{Leaf: "leaf-1"},
	}
}

func (f *fakeRuntime) Prompt(_ context.Context, in runtime.PromptInput) (runtime.Disposition, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, in)
	f.mu.Unlock()
	return runtime.DispositionStarted, nil
}
func (f *fakeRuntime) Steer(_ context.Context, m runtime.Message) error {
	f.mu.Lock()
	f.steers = append(f.steers, m)
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) FollowUp(_ context.Context, m runtime.Message) error {
	f.mu.Lock()
	f.followUps = append(f.followUps, m)
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Abort(context.Context) error {
	f.mu.Lock()
	f.aborted = true
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Subscribe(fn func(runtime.Event)) runtime.Unsubscribe {
	f.mu.Lock()
	id := f.nextSub
	f.nextSub++
	f.subs[id] = fn
	select {
	case <-f.subscribed:
	default:
		close(f.subscribed)
	}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.subs, id)
		f.mu.Unlock()
	}
}
func (f *fakeRuntime) State() runtime.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}
func (f *fakeRuntime) Entries(context.Context, runtime.EntryQuery) (*runtime.EntriesResult, error) {
	return f.entries, nil
}
func (f *fakeRuntime) Settled() <-chan struct{}    { return f.settled }
func (f *fakeRuntime) Close(context.Context) error { return nil }

func (f *fakeRuntime) emit(ev runtime.Event) {
	f.mu.Lock()
	subs := make([]func(runtime.Event), 0, len(f.subs))
	for _, fn := range f.subs {
		subs = append(subs, fn)
	}
	f.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func postRPC(t *testing.T, url string, req RuntimeRequest) RuntimeResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()
	var out RuntimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestRuntimeServerRPC(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(NewRuntimeServer(fake))
	defer srv.Close()

	got := postRPC(t, srv.URL, RuntimeRequest{
		ID:      "1",
		Command: "prompt",
		Data:    json.RawMessage(`{"text":"hello"}`),
	})
	if !got.Success || got.ID != "1" || got.Command != "prompt" {
		t.Fatalf("prompt response = %+v", got)
	}
	if len(fake.prompts) != 1 || fake.prompts[0].Text != "hello" {
		t.Fatalf("prompts = %+v", fake.prompts)
	}

	got = postRPC(t, srv.URL, RuntimeRequest{ID: "2", Command: "steer", Data: json.RawMessage(`{"text":"steer"}`)})
	if !got.Success || len(fake.steers) != 1 {
		t.Fatalf("steer response = %+v steers=%+v", got, fake.steers)
	}

	got = postRPC(t, srv.URL, RuntimeRequest{ID: "3", Command: "get_state"})
	if !got.Success {
		t.Fatalf("state response = %+v", got)
	}
	state, ok := got.Data.(map[string]any)
	if !ok || state["session_id"] != "fake-session" {
		t.Fatalf("state data = %#v", got.Data)
	}

	got = postRPC(t, srv.URL, RuntimeRequest{ID: "4", Command: "nope"})
	if got.Success || got.Error == "" {
		t.Fatalf("unknown command response = %+v", got)
	}
}

func TestRuntimeServerEvents(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(NewRuntimeServer(fake))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	select {
	case <-fake.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not subscribe")
	}

	fake.emit(runtime.Event{
		Type:      runtime.EventAgentStart,
		SessionID: "fake-session",
		Seq:       1,
	})

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("SSE line = %q", line)
	}
	var ev runtime.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &ev); err != nil {
		t.Fatalf("decode SSE event: %v", err)
	}
	if ev.Type != runtime.EventAgentStart || ev.SessionID != "fake-session" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestRuntimeServerHealth(t *testing.T) {
	srv := httptest.NewServer(NewRuntimeServer(newFakeRuntime()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
