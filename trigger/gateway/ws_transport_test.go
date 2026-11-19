package gateway

import (
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

// queueFakeRuntime adds the optional runtime.QueueController capability used by
// clear_queue to the shared fakeRuntime.
type queueFakeRuntime struct {
	*fakeRuntime
	steering []runtime.Message
	followUp []runtime.Message
}

func (f *queueFakeRuntime) ClearQueue(context.Context) ([]runtime.Message, []runtime.Message) {
	return f.steering, f.followUp
}

func dialTestWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func writeWSRequest(t *testing.T, conn *websocket.Conn, req RuntimeRequest) {
	t.Helper()
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readWSMessage(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if err := json.Unmarshal(payload, v); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
}

func readWSResponse(t *testing.T, conn *websocket.Conn) RuntimeResponse {
	t.Helper()
	var resp RuntimeResponse
	readWSMessage(t, conn, &resp)
	if resp.Type != "response" {
		t.Fatalf("message type = %q, want response (%+v)", resp.Type, resp)
	}
	return resp
}

func TestWebSocketPromptAndState(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(WebSocketHandler(fake))
	defer srv.Close()

	conn := dialTestWS(t, srv)

	writeWSRequest(t, conn, RuntimeRequest{
		ID:      "1",
		Command: "prompt",
		Data:    json.RawMessage(`{"text":"hello"}`),
	})
	resp := readWSResponse(t, conn)
	if !resp.Success || resp.ID != "1" || resp.Command != "prompt" {
		t.Fatalf("prompt response = %+v", resp)
	}
	disp, ok := resp.Data.(map[string]any)
	if !ok || disp["disposition"] != string(runtime.DispositionStarted) {
		t.Fatalf("prompt data = %#v", resp.Data)
	}

	fake.mu.Lock()
	prompts := append([]runtime.PromptInput(nil), fake.prompts...)
	fake.mu.Unlock()
	if len(prompts) != 1 || prompts[0].Text != "hello" {
		t.Fatalf("prompts = %+v", prompts)
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "2", Command: "get_state"})
	resp = readWSResponse(t, conn)
	if !resp.Success || resp.ID != "2" {
		t.Fatalf("state response = %+v", resp)
	}
	state, ok := resp.Data.(map[string]any)
	if !ok || state["session_id"] != "fake-session" {
		t.Fatalf("state data = %#v", resp.Data)
	}
}

func TestWebSocketRemainingCommands(t *testing.T) {
	fake := newFakeRuntime()
	queue := &queueFakeRuntime{
		fakeRuntime: fake,
		steering:    []runtime.Message{{Role: "user", Text: "steer"}},
		followUp:    []runtime.Message{{Role: "user", Text: "follow"}},
	}
	srv := httptest.NewServer(WebSocketHandler(queue))
	defer srv.Close()

	conn := dialTestWS(t, srv)

	writeWSRequest(t, conn, RuntimeRequest{ID: "1", Command: "steer", Data: json.RawMessage(`{"text":"s"}`)})
	if resp := readWSResponse(t, conn); !resp.Success || resp.ID != "1" {
		t.Fatalf("steer response = %+v", resp)
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "2", Command: "follow_up", Data: json.RawMessage(`{"text":"f"}`)})
	if resp := readWSResponse(t, conn); !resp.Success || resp.ID != "2" {
		t.Fatalf("follow_up response = %+v", resp)
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "3", Command: "abort"})
	if resp := readWSResponse(t, conn); !resp.Success || resp.ID != "3" {
		t.Fatalf("abort response = %+v", resp)
	}
	fake.mu.Lock()
	aborted := fake.aborted
	fake.mu.Unlock()
	if !aborted {
		t.Fatal("runtime was not aborted")
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "4", Command: "get_entries"})
	resp := readWSResponse(t, conn)
	entries, ok := resp.Data.(map[string]any)
	if !resp.Success || !ok || entries["leaf"] != "leaf-1" {
		t.Fatalf("get_entries response = %+v", resp)
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "5", Command: "settled"})
	if resp := readWSResponse(t, conn); !resp.Success {
		t.Fatalf("settled response = %+v", resp)
	}

	writeWSRequest(t, conn, RuntimeRequest{ID: "6", Command: "clear_queue"})
	resp = readWSResponse(t, conn)
	data, ok := resp.Data.(map[string]any)
	if !resp.Success || !ok {
		t.Fatalf("clear_queue response = %+v", resp)
	}
	if got, _ := data["steering"].([]any); len(got) != 1 {
		t.Fatalf("clear_queue data = %#v", resp.Data)
	}
}

func TestWebSocketUnknownCommand(t *testing.T) {
	srv := httptest.NewServer(WebSocketHandler(newFakeRuntime()))
	defer srv.Close()

	conn := dialTestWS(t, srv)
	writeWSRequest(t, conn, RuntimeRequest{ID: "9", Command: "nope"})
	resp := readWSResponse(t, conn)
	if resp.Success || resp.ID != "9" || !strings.Contains(resp.Error, "unknown command") {
		t.Fatalf("unknown command response = %+v", resp)
	}
}

func TestWebSocketParseError(t *testing.T) {
	srv := httptest.NewServer(WebSocketHandler(newFakeRuntime()))
	defer srv.Close()

	conn := dialTestWS(t, srv)
	if err := conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readWSResponse(t, conn)
	if resp.Success || resp.Command != "parse" || resp.Error == "" {
		t.Fatalf("parse error response = %+v", resp)
	}
}

func TestWebSocketEventPush(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(WebSocketHandler(fake))
	defer srv.Close()

	conn := dialTestWS(t, srv)

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

	var msg wsEventMessage
	readWSMessage(t, conn, &msg)
	if msg.Type != "event" {
		t.Fatalf("message type = %q, want event", msg.Type)
	}
	if msg.Event.Type != runtime.EventAgentStart || msg.Event.SessionID != "fake-session" {
		t.Fatalf("event = %+v", msg.Event)
	}
}

func TestWebSocketCloseUnsubscribes(t *testing.T) {
	fake := newFakeRuntime()
	srv := httptest.NewServer(WebSocketHandler(fake))
	defer srv.Close()

	conn := dialTestWS(t, srv)
	select {
	case <-fake.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not subscribe")
	}

	fake.mu.Lock()
	before := len(fake.subs)
	fake.mu.Unlock()
	if before != 1 {
		t.Fatalf("subscriptions = %d, want 1", before)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		fake.mu.Lock()
		n := len(fake.subs)
		fake.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscription was not removed (remaining %d)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebSocketEnqueueEventDropsWhenFull(t *testing.T) {
	s := &wsServer{}
	outbound := make(chan []byte, 1)
	connDone := make(chan struct{})
	ev := runtime.Event{Type: runtime.EventAgentStart}

	s.enqueueEvent(outbound, connDone, ev)
	if len(outbound) != 1 {
		t.Fatalf("first event not enqueued")
	}
	// A slow client must not block the session: excess events are dropped.
	s.enqueueEvent(outbound, connDone, ev)
	if len(outbound) != 1 {
		t.Fatalf("second event should have been dropped, buffer = %d", len(outbound))
	}

	close(connDone)
	s.enqueueEvent(outbound, connDone, ev)
	if len(outbound) != 1 {
		t.Fatalf("event enqueued after connection close")
	}
}

func TestWebSocketMethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(WebSocketHandler(newFakeRuntime()))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ws", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /ws: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}
