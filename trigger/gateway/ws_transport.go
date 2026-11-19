package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xichan96/cortex/agent/runtime"
)

// Defaults for the WebSocket runtime transport.
const (
	defaultWSOutboundBuffer = 256
	defaultWSWriteTimeout   = 10 * time.Second
	defaultWSReadLimit      = 4 << 20
	defaultWSHeartbeat      = 30 * time.Second

	wsParseCommand = "parse"
)

// WebSocketOptions configures the WebSocket runtime transport. The zero value
// uses local-development defaults (see WebSocketHandler).
type WebSocketOptions struct {
	// Upgrader performs the HTTP upgrade. Nil uses a local-development
	// upgrader that accepts same-origin and localhost origins.
	Upgrader *websocket.Upgrader
	// HeartbeatInterval is how often the server pings the client. Zero selects
	// the default (30s); a negative value disables heartbeats.
	HeartbeatInterval time.Duration
	// WriteTimeout bounds a single frame write. Zero selects the default (10s).
	WriteTimeout time.Duration
	// ReadLimit bounds one inbound message in bytes. Zero selects the default
	// (4 MiB).
	ReadLimit int64
	// OutboundBuffer is the per-connection event queue. Events are dropped
	// when the client cannot keep up. Zero selects the default (256).
	OutboundBuffer int
}

// WebSocketHandler exposes rt over a single WebSocket endpoint:
//
//	GET /ws  bidirectional JSON:
//	         client -> {id, command, data}
//	         server -> {id, type:"response", command, success, data|error}
//	                   {type:"event", event}
//
// Commands match the HTTP RuntimeServer: prompt, steer, follow_up, abort,
// get_state, get_entries, settled and clear_queue. Commands run concurrently
// and responses are correlated by id. Each connection owns one event
// subscription; events are best-effort for slow clients (they can recover the
// authoritative log with get_entries).
func WebSocketHandler(rt runtime.Runtime) http.Handler {
	return NewWebSocketHandler(rt, WebSocketOptions{})
}

// NewWebSocketHandler is WebSocketHandler with explicit options.
func NewWebSocketHandler(rt runtime.Runtime, opts WebSocketOptions) http.Handler {
	if opts.Upgrader == nil {
		upgrader := NewLocalDevWebSocketUpgrader()
		opts.Upgrader = &upgrader
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = defaultWSHeartbeat
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWSWriteTimeout
	}
	if opts.ReadLimit <= 0 {
		opts.ReadLimit = defaultWSReadLimit
	}
	if opts.OutboundBuffer <= 0 {
		opts.OutboundBuffer = defaultWSOutboundBuffer
	}
	return &wsServer{rt: rt, opts: opts}
}

type wsServer struct {
	rt   runtime.Runtime
	opts WebSocketOptions
}

// wsEventMessage is the server -> client event envelope.
type wsEventMessage struct {
	Type  string        `json:"type"`
	Event runtime.Event `json:"event"`
}

func (s *wsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.rt == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "runtime unavailable"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	conn, err := s.opts.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		return
	}
	s.serveConn(conn)
}

func (s *wsServer) serveConn(conn *websocket.Conn) {
	conn.SetReadLimit(s.opts.ReadLimit)

	if s.opts.HeartbeatInterval > 0 {
		pongWait := 3 * s.opts.HeartbeatInterval
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
	}

	var (
		outbound    = make(chan []byte, s.opts.OutboundBuffer)
		connDone    = make(chan struct{})
		stopWrite   = make(chan struct{})
		writerDone  = make(chan struct{})
		ctx, cancel = context.WithCancel(context.Background())
	)
	defer cancel()

	unsub := s.rt.Subscribe(func(ev runtime.Event) {
		s.enqueueEvent(outbound, connDone, ev)
	})
	defer unsub()

	go s.writeLoop(conn, outbound, stopWrite, writerDone, cancel)

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var req RuntimeRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			s.sendResponse(ctx, outbound, RuntimeResponse{
				Type:    "response",
				Command: wsParseCommand,
				Error:   "invalid request: " + err.Error(),
			})
			continue
		}
		go s.handleCommand(ctx, outbound, req)
	}

	close(connDone)
	cancel()
	close(stopWrite)
	<-writerDone
	_ = conn.Close()
}

func (s *wsServer) handleCommand(ctx context.Context, outbound chan<- []byte, req RuntimeRequest) {
	resp := RuntimeResponse{
		ID:      req.ID,
		Type:    "response",
		Command: req.Command,
	}
	data, err := dispatchRuntimeCommand(ctx, s.rt, req)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Success = true
		resp.Data = data
	}
	s.sendResponse(ctx, outbound, resp)
}

// sendResponse enqueues a command response. Responses are never dropped for a
// healthy connection; the send is released only when the connection closes.
func (s *wsServer) sendResponse(ctx context.Context, outbound chan<- []byte, resp RuntimeResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	select {
	case outbound <- b:
	case <-ctx.Done():
	}
}

// enqueueEvent publishes one runtime event. Slow clients drop events instead
// of blocking the session; the authoritative state remains available through
// get_entries and message_end.
func (s *wsServer) enqueueEvent(outbound chan<- []byte, connDone <-chan struct{}, ev runtime.Event) {
	select {
	case <-connDone:
		return
	default:
	}
	b, err := json.Marshal(wsEventMessage{Type: "event", Event: ev})
	if err != nil {
		return
	}
	select {
	case outbound <- b:
	default:
	}
}

func (s *wsServer) writeLoop(conn *websocket.Conn, outbound <-chan []byte, stop <-chan struct{}, done chan<- struct{}, cancel context.CancelFunc) {
	defer cancel()
	defer close(done)
	// Closing the connection here also unblocks a reader waiting on a client
	// that stopped consuming frames.
	defer conn.Close()

	var tick <-chan time.Time
	if s.opts.HeartbeatInterval > 0 {
		ticker := time.NewTicker(s.opts.HeartbeatInterval)
		defer ticker.Stop()
		tick = ticker.C
	}

	for {
		select {
		case <-stop:
			return
		case msg := <-outbound:
			_ = conn.SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-tick:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.opts.WriteTimeout)); err != nil {
				return
			}
		}
	}
}
