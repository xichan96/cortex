package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/runtime"
)

// RuntimeServer exposes an agent/runtime.Runtime over HTTP:
//
//	POST /rpc         one command per request, JSON {id,command,data}
//	GET  /events      Server-Sent Events stream of runtime events
//	GET  /ui/requests Server-Sent Events stream of approval/question/plan requests
//	POST /ui/respond  answer a pending UI request by ID
//	GET  /health      liveness probe
//
// The /ui/* endpoints are only available when an approval.InteractiveBridge is
// configured with NewRuntimeServerWithBridge or SetBridge.
//
// It is transport-neutral with respect to the agent: any runtime (dino
// SessionRuntime, MemoryRuntime, an embedded SDK runtime) can be mounted here.
type RuntimeServer struct {
	rt runtime.Runtime

	mu     sync.RWMutex
	bridge approval.InteractiveBridge
}

// NewRuntimeServer wraps rt in an HTTP handler.
func NewRuntimeServer(rt runtime.Runtime) *RuntimeServer {
	return &RuntimeServer{rt: rt}
}

// NewRuntimeServerWithBridge wraps rt and enables the /ui/* endpoints backed
// by bridge.
func NewRuntimeServerWithBridge(rt runtime.Runtime, bridge approval.InteractiveBridge) *RuntimeServer {
	return &RuntimeServer{rt: rt, bridge: bridge}
}

// RuntimeRequest is the POST /rpc body.
type RuntimeRequest struct {
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// RuntimeResponse is the POST /rpc response.
type RuntimeResponse struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *RuntimeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/rpc":
		s.handleRPC(w, r)
	case "/events":
		s.handleEvents(w, r)
	case "/ui/requests":
		s.handleUIRequests(w, r)
	case "/ui/respond":
		s.handleUIRespond(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *RuntimeServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var req RuntimeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request: " + err.Error()})
		return
	}
	resp := RuntimeResponse{ID: req.ID, Type: "response", Command: req.Command}
	data, err := s.dispatch(r, req)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Success = true
	resp.Data = data
	writeJSON(w, http.StatusOK, resp)
}

func (s *RuntimeServer) dispatch(r *http.Request, req RuntimeRequest) (any, error) {
	ctx := r.Context()
	switch req.Command {
	case "prompt":
		var in runtime.PromptInput
		if err := decode(req.Data, &in); err != nil {
			return nil, err
		}
		disp, err := s.rt.Prompt(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]any{"disposition": disp}, nil
	case "steer", "follow_up":
		var m runtime.Message
		if err := decode(req.Data, &m); err != nil {
			return nil, err
		}
		if req.Command == "steer" {
			return nil, s.rt.Steer(ctx, m)
		}
		return nil, s.rt.FollowUp(ctx, m)
	case "abort":
		return nil, s.rt.Abort(ctx)
	case "get_state":
		return s.rt.State(), nil
	case "get_entries":
		var q runtime.EntryQuery
		if len(req.Data) > 0 {
			if err := decode(req.Data, &q); err != nil {
				return nil, err
			}
		}
		return s.rt.Entries(ctx, q)
	case "settled":
		select {
		case <-s.rt.Settled():
			return map[string]any{"settled": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "subscribe":
		return nil, fmt.Errorf("use GET /events for subscriptions")
	default:
		return nil, fmt.Errorf("unknown command %q", req.Command)
	}
}

func (s *RuntimeServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events := make(chan runtime.Event, 64)
	unsub := s.rt.Subscribe(func(ev runtime.Event) {
		select {
		case events <- ev:
		default:
			// Slow client: drop rather than block the session. Runtime events
			// remain available through get_entries / authoritative message_end.
		}
	})
	defer unsub()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev := <-events:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("missing data")
	}
	return json.Unmarshal(raw, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
