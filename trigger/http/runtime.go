package http

import (
	"net/http"

	"github.com/xichan96/cortex/agent/runtime"
	"github.com/xichan96/cortex/trigger/gateway"
)

// RuntimeHandler exposes an agent/runtime.Runtime over plain net/http:
//
//	POST /rpc     one command per request, JSON {id,command,data}
//	GET  /events  Server-Sent Events stream of runtime events
//	GET  /ws      bidirectional WebSocket runtime transport
//	GET  /health  liveness probe
//
// It is the transport-neutral counterpart of the existing engine-based HTTP
// trigger and does not depend on dino or the agent engine. Mount it on a mux
// or StripPrefix router; the existing ChatAPI/StreamChatAPI handlers are
// unaffected.
func RuntimeHandler(rt runtime.Runtime) http.Handler {
	return &runtimeHandler{server: gateway.NewRuntimeServer(rt), ws: gateway.WebSocketHandler(rt)}
}

type runtimeHandler struct {
	server *gateway.RuntimeServer
	ws     http.Handler
}

func (h *runtimeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" {
		h.ws.ServeHTTP(w, r)
		return
	}
	h.server.ServeHTTP(w, r)
}
