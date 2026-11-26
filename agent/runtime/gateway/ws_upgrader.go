package gateway

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

func IsLocalDevOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func NewLocalDevWebSocketUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			return IsLocalDevOrigin(origin)
		},
		ReadBufferSize:  16384,
		WriteBufferSize: 16384,
	}
}
