package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xichan96/cortex/agent/runtime"
	"github.com/xichan96/cortex/trigger/gateway"
)

type fakeRuntime struct {
	mu         sync.Mutex
	prompts    []runtime.PromptInput
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
		state:      runtime.State{SessionID: "http-fake-session"},
		entries:    &runtime.EntriesResult{Leaf: "leaf-1"},
	}
}

func (f *fakeRuntime) Prompt(_ context.Context, in runtime.PromptInput) (runtime.Disposition, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, in)
	f.mu.Unlock()
	return runtime.DispositionStarted, nil
}

func (f *fakeRuntime) Steer(context.Context, runtime.Message) error    { return nil }
func (f *fakeRuntime) FollowUp(context.Context, runtime.Message) error { return nil }
func (f *fakeRuntime) Abort(context.Context) error                     { return nil }

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

func TestRuntimeHandlerRPC(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(RuntimeHandler(fake))
	defer srv.Close()

	body, err := json.Marshal(gateway.RuntimeRequest{
		ID:      "1",
		Command: "prompt",
		Data:    json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := stdhttp.Post(srv.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()

	var out gateway.RuntimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Success || out.ID != "1" || out.Command != "prompt" {
		t.Fatalf("prompt response = %+v", out)
	}
	if len(fake.prompts) != 1 || fake.prompts[0].Text != "hello" {
		t.Fatalf("prompts = %+v", fake.prompts)
	}

	// Unknown commands fail without breaking the handler.
	body, _ = json.Marshal(gateway.RuntimeRequest{ID: "2", Command: "nope"})
	resp2, err := stdhttp.Post(srv.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc unknown: %v", err)
	}
	defer resp2.Body.Close()
	out = gateway.RuntimeResponse{}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode unknown: %v", err)
	}
	if out.Success || out.Error == "" {
		t.Fatalf("unknown command response = %+v", out)
	}
}

func TestRuntimeHandlerEvents(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(RuntimeHandler(fake))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	select {
	case <-fake.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not subscribe")
	}

	fake.emit(runtime.Event{
		Type:      runtime.EventMessageEnd,
		SessionID: "http-fake-session",
		Seq:       1,
	})

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("SSE line = %q", line)
	}
	var ev runtime.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.Type != runtime.EventMessageEnd || ev.SessionID != "http-fake-session" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestRuntimeHandlerHealth(t *testing.T) {
	srv := httptest.NewServer(RuntimeHandler(newFakeRuntime()))
	defer srv.Close()

	resp, err := stdhttp.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Fatalf("body = %#v", body)
	}
}

func TestRuntimeHandlerWebSocket(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(RuntimeHandler(fake))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(gateway.RuntimeRequest{
		ID:      "1",
		Command: "prompt",
		Data:    json.RawMessage(`{"text":"via ws"}`),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var out gateway.RuntimeResponse
	if err := conn.ReadJSON(&out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !out.Success || out.ID != "1" {
		t.Fatalf("response = %+v", out)
	}
	if len(fake.prompts) != 1 || fake.prompts[0].Text != "via ws" {
		t.Fatalf("prompts = %+v", fake.prompts)
	}
}
