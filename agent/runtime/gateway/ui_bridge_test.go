package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/approval"
)

// fakeUIBridge records responses and exposes a hand-fed request stream. Its
// Request method is not used by the gateway.
type fakeUIBridge struct {
	requests chan approval.BridgeRequest

	mu         sync.Mutex
	responses  []approval.BridgeResponse
	respondErr error
}

func newFakeUIBridge() *fakeUIBridge {
	return &fakeUIBridge{requests: make(chan approval.BridgeRequest, 8)}
}

func (f *fakeUIBridge) Request(context.Context, approval.BridgeRequest) (approval.BridgeResponse, error) {
	return approval.BridgeResponse{}, errors.New("fake bridge Request is not used by the gateway")
}

func (f *fakeUIBridge) Requests() <-chan approval.BridgeRequest { return f.requests }

func (f *fakeUIBridge) Respond(resp approval.BridgeResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.respondErr != nil {
		return f.respondErr
	}
	f.responses = append(f.responses, resp)
	return nil
}

func (f *fakeUIBridge) lastResponse() (approval.BridgeResponse, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return approval.BridgeResponse{}, false
	}
	return f.responses[len(f.responses)-1], true
}

func TestRuntimeServerUIRequestsStream(t *testing.T) {
	fakeBridge := newFakeUIBridge()
	srv := httptest.NewServer(NewRuntimeServerWithBridge(newFakeRuntime(), fakeBridge))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ui/requests", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	sent := approval.BridgeRequest{
		ID:        "req-1",
		Kind:      approval.KindApproval,
		SessionID: "session-1",
		Title:     "Run Bash?",
		Payload:   json.RawMessage(`{"tool":"Bash"}`),
	}
	fakeBridge.requests <- sent

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("SSE line = %q", line)
	}
	var got approval.BridgeRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &got); err != nil {
		t.Fatalf("decode SSE request: %v", err)
	}
	if got.ID != sent.ID || got.Kind != sent.Kind || got.SessionID != sent.SessionID || got.Title != sent.Title {
		t.Fatalf("request = %+v", got)
	}
	if string(got.Payload) != string(sent.Payload) {
		t.Fatalf("payload = %s, want %s", got.Payload, sent.Payload)
	}
}

func TestRuntimeServerUIRespondDeliversToWaiter(t *testing.T) {
	bridge := approval.NewMemoryBridge()
	defer bridge.Close()
	srv := httptest.NewServer(NewRuntimeServerWithBridge(newFakeRuntime(), bridge))
	defer srv.Close()

	type result struct {
		resp approval.BridgeResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := bridge.Request(context.Background(), approval.BridgeRequest{
			Kind:    approval.KindQuestion,
			Title:   "Which file?",
			Timeout: 3 * time.Second,
		})
		done <- result{resp: resp, err: err}
	}()

	var req approval.BridgeRequest
	select {
	case req = <-bridge.Requests():
	case <-time.After(2 * time.Second):
		t.Fatal("no bridge request")
	}

	value := json.RawMessage(`{"answer":"main.go"}`)
	body, err := json.Marshal(approval.BridgeResponse{ID: req.ID, Value: value})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	postResp, err := http.Post(srv.URL+"/ui/respond", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /ui/respond: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", postResp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(postResp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["ok"] != true || out["id"] != req.ID {
		t.Fatalf("response body = %#v", out)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("waiter error: %v", res.err)
		}
		if res.resp.ID != req.ID || string(res.resp.Value) != string(value) {
			t.Fatalf("waiter response = %+v", res.resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not receive the response")
	}
}

func TestRuntimeServerUIRespondUnknownID(t *testing.T) {
	bridge := approval.NewMemoryBridge()
	defer bridge.Close()
	srv := httptest.NewServer(NewRuntimeServerWithBridge(newFakeRuntime(), bridge))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/respond", "application/json", strings.NewReader(`{"id":"missing","approved":true}`))
	if err != nil {
		t.Fatalf("POST /ui/respond: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/ui/respond", "application/json", strings.NewReader(`{"approved":true}`))
	if err != nil {
		t.Fatalf("POST /ui/respond (no id): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRuntimeServerUIWithoutBridge(t *testing.T) {
	srv := httptest.NewServer(NewRuntimeServer(newFakeRuntime()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/requests")
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == "" || body["error"] == nil {
		t.Fatalf("expected explicit error, got %#v", body)
	}

	resp, err = http.Post(srv.URL+"/ui/respond", "application/json", strings.NewReader(`{"id":"x","approved":true}`))
	if err != nil {
		t.Fatalf("POST /ui/respond: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRuntimeServerSetBridge(t *testing.T) {
	fakeBridge := newFakeUIBridge()
	srv := httptest.NewServer(NewRuntimeServer(newFakeRuntime()))
	defer srv.Close()
	server := srv.Config.Handler.(*RuntimeServer)

	resp, err := http.Get(srv.URL + "/ui/requests")
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status before SetBridge = %d, want 404", resp.StatusCode)
	}

	server.SetBridge(fakeBridge)
	if server.Bridge() == nil {
		t.Fatal("Bridge() = nil after SetBridge")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ui/requests", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("status after SetBridge = %d, want 200", streamResp.StatusCode)
	}

	server.SetBridge(nil)
	if server.Bridge() != nil {
		t.Fatal("Bridge() != nil after SetBridge(nil)")
	}
	resp, err = http.Get(srv.URL + "/ui/requests")
	if err != nil {
		t.Fatalf("GET /ui/requests after reset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status after SetBridge(nil) = %d, want 404", resp.StatusCode)
	}
}

func TestRuntimeServerUIMethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(NewRuntimeServerWithBridge(newFakeRuntime(), newFakeUIBridge()))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/requests", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /ui/requests: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/ui/respond")
	if err != nil {
		t.Fatalf("GET /ui/respond: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}
