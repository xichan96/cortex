package gateway

import (
	"net/http"

	"github.com/xichan96/cortex/agent/runtime"
)

// RuntimeHandler exposes an agent/runtime.Runtime over plain net/http:
//
//	POST /rpc     one command per request, JSON {id,command,data}
//	GET  /events  Server-Sent Events stream of runtime events
//	GET  /ws      bidirectional WebSocket runtime transport
//	GET  /health  liveness probe
//
// It depends only on runtime.Runtime, not on dino or the agent engine. Mount it
// on a mux or StripPrefix router; other transport handlers are unaffected.
func RuntimeHandler(rt runtime.Runtime) http.Handler {
	return &runtimeHandler{server: NewRuntimeServer(rt), ws: WebSocketHandler(rt)}
}

type runtimeHandler struct {
	server *RuntimeServer
	ws     http.Handler
}

func (h *runtimeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" {
		h.ws.ServeHTTP(w, r)
		return
	}
	h.server.ServeHTTP(w, r)
}
