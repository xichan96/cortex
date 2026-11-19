package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type bridgeResult struct {
	resp BridgeResponse
	err  error
}

func startBridgeRequest(b Bridge, req BridgeRequest) <-chan bridgeResult {
	done := make(chan bridgeResult, 1)
	go func() {
		resp, err := b.Request(context.Background(), req)
		done <- bridgeResult{resp: resp, err: err}
	}()
	return done
}

func waitRequest(t *testing.T, ch <-chan BridgeRequest) BridgeRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a bridge request")
		return BridgeRequest{}
	}
}

func waitResult(t *testing.T, ch <-chan bridgeResult) bridgeResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a bridge response")
		return bridgeResult{}
	}
}

func TestMemoryBridgeRoundTrip(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	payload := json.RawMessage(`{"tool":"bash","args":{"command":"ls"}}`)
	done := startBridgeRequest(b, BridgeRequest{
		Kind:      KindApproval,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Title:     "Run bash?",
		Payload:   payload,
		Timeout:   2 * time.Second,
	})

	req := waitRequest(t, b.Requests())
	if req.ID == "" {
		t.Fatal("request has no generated id")
	}
	if req.Kind != KindApproval || req.SessionID != "session-1" || req.TurnID != "turn-1" || req.Title != "Run bash?" {
		t.Fatalf("request = %+v", req)
	}
	if string(req.Payload) != string(payload) {
		t.Fatalf("payload = %s, want %s", req.Payload, payload)
	}

	value := json.RawMessage(`{"scope":"session"}`)
	if err := b.Respond(BridgeResponse{ID: req.ID, Approved: true, Value: value}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	res := waitResult(t, done)
	if res.err != nil {
		t.Fatalf("request returned error: %v", res.err)
	}
	if res.resp.ID != req.ID || !res.resp.Approved || res.resp.Cancelled {
		t.Fatalf("response = %+v", res.resp)
	}
	if string(res.resp.Value) != string(value) {
		t.Fatalf("value = %s, want %s", res.resp.Value, value)
	}
}

func TestMemoryBridgeGeneratedIDsAreUnique(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	first := startBridgeRequest(b, BridgeRequest{Kind: KindQuestion, Timeout: 2 * time.Second})
	second := startBridgeRequest(b, BridgeRequest{Kind: KindQuestion, Timeout: 2 * time.Second})

	req1 := waitRequest(t, b.Requests())
	req2 := waitRequest(t, b.Requests())
	if req1.ID == "" || req2.ID == "" || req1.ID == req2.ID {
		t.Fatalf("ids = %q, %q", req1.ID, req2.ID)
	}
	_ = b.Respond(BridgeResponse{ID: req1.ID, Cancelled: true})
	_ = b.Respond(BridgeResponse{ID: req2.ID, Cancelled: true})
	waitResult(t, first)
	waitResult(t, second)
}

func TestMemoryBridgeTimeout(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	start := time.Now()
	resp, err := b.Request(context.Background(), BridgeRequest{
		Kind:    KindQuestion,
		Timeout: 40 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrBridgeTimeout) {
		t.Fatalf("err = %v, want ErrBridgeTimeout", err)
	}
	if resp.ID == "" {
		t.Fatal("timeout response has no id")
	}
	if resp.Cancelled {
		t.Fatalf("timeout should not be reported as cancelled: %+v", resp)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("returned too early after %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}

	// The request is still queued once; drain it so the test leaves no work.
	select {
	case <-b.Requests():
	default:
	}
}

func TestMemoryBridgeContextCancel(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bridgeResult, 1)
	go func() {
		resp, err := b.Request(ctx, BridgeRequest{Kind: KindPlan, Timeout: 2 * time.Second})
		done <- bridgeResult{resp: resp, err: err}
	}()

	req := waitRequest(t, b.Requests())
	cancel()

	res := waitResult(t, done)
	if !errors.Is(res.err, ErrBridgeCancelled) {
		t.Fatalf("err = %v, want ErrBridgeCancelled", res.err)
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", res.err)
	}
	if res.resp.ID != req.ID || !res.resp.Cancelled {
		t.Fatalf("response = %+v", res.resp)
	}
}

func TestMemoryBridgeUICancelled(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	done := startBridgeRequest(b, BridgeRequest{Kind: KindApproval, Timeout: 2 * time.Second})
	req := waitRequest(t, b.Requests())
	if err := b.Respond(BridgeResponse{ID: req.ID, Cancelled: true}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	res := waitResult(t, done)
	if res.err != nil {
		t.Fatalf("cancelled response returned error: %v", res.err)
	}
	if !res.resp.Cancelled || res.resp.Approved {
		t.Fatalf("response = %+v", res.resp)
	}
}

func TestMemoryBridgeUnknownID(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	if err := b.Respond(BridgeResponse{ID: "missing", Approved: true}); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("err = %v, want ErrUnknownRequest", err)
	}
	if err := b.Respond(BridgeResponse{}); !errors.Is(err, ErrInvalidBridgeRequest) {
		t.Fatalf("err = %v, want ErrInvalidBridgeRequest", err)
	}

	// A response is delivered at most once.
	done := startBridgeRequest(b, BridgeRequest{Kind: KindApproval, Timeout: 2 * time.Second})
	req := waitRequest(t, b.Requests())
	if err := b.Respond(BridgeResponse{ID: req.ID, Approved: true}); err != nil {
		t.Fatalf("first respond: %v", err)
	}
	waitResult(t, done)
	if err := b.Respond(BridgeResponse{ID: req.ID, Approved: false}); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("second respond err = %v, want ErrUnknownRequest", err)
	}
}

func TestMemoryBridgeDuplicateID(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan bridgeResult, 1)
	go func() {
		resp, err := b.Request(ctx, BridgeRequest{ID: "fixed-id", Kind: KindApproval})
		first <- bridgeResult{resp: resp, err: err}
	}()
	waitRequest(t, b.Requests())

	_, err := b.Request(context.Background(), BridgeRequest{ID: "fixed-id", Kind: KindApproval})
	if !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("err = %v, want ErrDuplicateRequest", err)
	}

	cancel()
	<-first
}

func TestMemoryBridgeInvalidKind(t *testing.T) {
	b := NewMemoryBridge()
	defer b.Close()

	for _, kind := range []RequestKind{"", "bogus"} {
		_, err := b.Request(context.Background(), BridgeRequest{Kind: kind, Timeout: time.Second})
		if !errors.Is(err, ErrInvalidBridgeRequest) {
			t.Fatalf("kind %q: err = %v, want ErrInvalidBridgeRequest", kind, err)
		}
	}
}

func TestMemoryBridgeCloseUnblocks(t *testing.T) {
	b := NewMemoryBridge()

	done := startBridgeRequest(b, BridgeRequest{Kind: KindQuestion, Timeout: 2 * time.Second})
	waitRequest(t, b.Requests())

	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	res := waitResult(t, done)
	if !errors.Is(res.err, ErrBridgeClosed) {
		t.Fatalf("err = %v, want ErrBridgeClosed", res.err)
	}

	_, err := b.Request(context.Background(), BridgeRequest{Kind: KindQuestion})
	if !errors.Is(err, ErrBridgeClosed) {
		t.Fatalf("request after close err = %v, want ErrBridgeClosed", err)
	}
}

func TestChannelBridgeRoundTrip(t *testing.T) {
	b := NewChannelBridge()
	defer b.Close()

	done := startBridgeRequest(b, BridgeRequest{Kind: KindPlan, Title: "plan?", Timeout: 2 * time.Second})
	req := waitRequest(t, b.Requests())

	value := json.RawMessage(`{"steps":["one"]}`)
	b.Responses() <- BridgeResponse{ID: req.ID, Value: value}

	res := waitResult(t, done)
	if res.err != nil {
		t.Fatalf("request returned error: %v", res.err)
	}
	if res.resp.ID != req.ID || string(res.resp.Value) != string(value) {
		t.Fatalf("response = %+v", res.resp)
	}
}

func TestChannelBridgeRespond(t *testing.T) {
	b := NewChannelBridge()
	defer b.Close()

	done := startBridgeRequest(b, BridgeRequest{Kind: KindQuestion, Timeout: 2 * time.Second})
	req := waitRequest(t, b.Requests())
	if err := b.Respond(BridgeResponse{ID: req.ID, Value: json.RawMessage(`{"answer":"yes"}`)}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	res := waitResult(t, done)
	if res.err != nil || res.resp.ID != req.ID {
		t.Fatalf("result = %+v err=%v", res.resp, res.err)
	}

	if err := b.Respond(BridgeResponse{ID: "missing"}); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("unknown respond err = %v, want ErrUnknownRequest", err)
	}
}

func TestChannelBridgeTimeoutAndClose(t *testing.T) {
	b := NewChannelBridge()

	if _, err := b.Request(context.Background(), BridgeRequest{Kind: KindQuestion, Timeout: 30 * time.Millisecond}); !errors.Is(err, ErrBridgeTimeout) {
		t.Fatalf("err = %v, want ErrBridgeTimeout", err)
	}

	done := startBridgeRequest(b, BridgeRequest{Kind: KindQuestion, Timeout: 2 * time.Second})
	waitRequest(t, b.Requests())
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	res := waitResult(t, done)
	if !errors.Is(res.err, ErrBridgeClosed) {
		t.Fatalf("err = %v, want ErrBridgeClosed", res.err)
	}
}
