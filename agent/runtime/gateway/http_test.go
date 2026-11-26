package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xichan96/cortex/agent/runtime"
)

// The tests in this file exercise RuntimeHandler as a composition: one
// http.Handler must route /rpc, /events, /health and /ws to the same runtime.
// The individual endpoints are covered by runtime_server_test.go and
// ws_transport_test.go.

func TestRuntimeHandlerRPC(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(RuntimeHandler(fake))
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

	got = postRPC(t, srv.URL, RuntimeRequest{ID: "2", Command: "nope"})
	if got.Success || got.Error == "" {
		t.Fatalf("unknown command response = %+v", got)
	}
}

func TestRuntimeHandlerEvents(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(RuntimeHandler(fake))
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

	select {
	case <-fake.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not subscribe")
	}

	fake.emit(runtime.Event{
		Type:      runtime.EventMessageEnd,
		SessionID: "fake-session",
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
	if ev.Type != runtime.EventMessageEnd || ev.SessionID != "fake-session" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestRuntimeHandlerHealth(t *testing.T) {
	srv := httptest.NewServer(RuntimeHandler(newFakeRuntime()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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

	if err := conn.WriteJSON(RuntimeRequest{
		ID:      "1",
		Command: "prompt",
		Data:    json.RawMessage(`{"text":"via ws"}`),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var out RuntimeResponse
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
