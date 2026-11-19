package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/approval"
)

// uiKeepAlive is the SSE comment interval for idle /ui/requests connections.
const uiKeepAlive = 15 * time.Second

// SetBridge installs or replaces the bridge used by /ui/requests and
// /ui/respond. Passing nil disables both endpoints with a 404. SetBridge is
// safe to call while the server is serving requests, but a stream already
// open keeps reading from the bridge it started with.
func (s *RuntimeServer) SetBridge(bridge approval.InteractiveBridge) {
	s.mu.Lock()
	s.bridge = bridge
	s.mu.Unlock()
}

// Bridge returns the configured UI bridge, or nil when the endpoints are
// disabled.
func (s *RuntimeServer) Bridge() approval.InteractiveBridge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bridge
}

// handleUIRequests streams pending BridgeRequests as Server-Sent Events. A
// request is delivered to exactly one connected client; there is no replay.
func (s *RuntimeServer) handleUIRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	bridge := s.Bridge()
	if bridge == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no UI bridge configured"})
		return
	}
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

	requests := bridge.Requests()
	keepAlive := time.NewTicker(uiKeepAlive)
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
		case req, ok := <-requests:
			if !ok {
				return
			}
			data, err := json.Marshal(req)
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

// handleUIRespond accepts one BridgeResponse and hands it to the waiting
// caller. Unknown IDs return 404 so a stale UI can detect its mistake.
func (s *RuntimeServer) handleUIRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	bridge := s.Bridge()
	if bridge == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no UI bridge configured"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var resp approval.BridgeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid response: " + err.Error()})
		return
	}
	if strings.TrimSpace(resp.ID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "response id is required"})
		return
	}
	if err := bridge.Respond(resp); err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, approval.ErrUnknownRequest):
			status = http.StatusNotFound
		case errors.Is(err, approval.ErrBridgeClosed):
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": resp.ID})
}
