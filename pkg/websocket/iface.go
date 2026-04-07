// Package websocket implements a simple websocket connection manager.
package websocket

// ConnManagerIface is the interface for the connection manager.
type ConnManagerIface interface {
	Register(cli *Client)
	Unregister(cli *Client)
	Heartbeat(cli *Client)
	DestroyTask()
	BroadcastSession(sessionID string, msg []byte)
}
