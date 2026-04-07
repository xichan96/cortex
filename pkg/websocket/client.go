// Package websocket implements a simple websocket client.
package websocket

import (
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xichan96/cortex/pkg/logger"
)

// Callback is the callback function for processing messages.
type Callback func(client *Client, message []byte)

// Client is simple ws cli
type Client struct {
	Addr         string // client address
	User         string
	Socket       *websocket.Conn // user connection
	IsActive     bool
	Send         chan []byte // data to send
	AccessTime   int64       // access timestamp
	OnDisconnect func(*Client)
}

// NewClient initializes a new client.
func NewClient(addr string, user string, socket *websocket.Conn) (client *Client) {
	return &Client{
		Addr:       addr,
		User:       user,
		Socket:     socket,
		IsActive:   true,
		Send:       make(chan []byte, 2048),
		AccessTime: time.Now().Unix(),
	}
}

// Read reads messages from the client.
func (c *Client) Read(processData Callback) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("read stop", slog.Any("recover", r))
		}
		c.IsActive = false
		close(c.Send)
		if c.OnDisconnect != nil {
			c.OnDisconnect(c)
			c.OnDisconnect = nil
		}
		if WsServer != nil {
			WsServer.Unregister <- c
		}
	}()

	for {
		_, message, err := c.Socket.ReadMessage()
		if err != nil {
			logger.Error("websocket read error",
				slog.Any("error", err),
				slog.String("host", c.Addr),
				slog.String("user", c.User),
			)
			return
		}
		processData(c, message)
	}
}

// Write writes messages to the client.
func (c *Client) Write() {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("write stop", slog.Any("recover", r))
		}
		c.IsActive = false
		c.Socket.Close()
		if c.OnDisconnect != nil {
			c.OnDisconnect(c)
			c.OnDisconnect = nil
		}
		if WsServer != nil {
			WsServer.Unregister <- c
		}
	}()
	for message := range c.Send {
		if err := c.Socket.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
}

// SendMsg sends a message to the client.
func (c *Client) SendMsg(msg []byte) {
	if c == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.Error("SendMsg panic, client may be closed",
				slog.String("user", c.User),
				slog.Any("recover", r),
			)
		}
	}()
	select {
	case c.Send <- msg:
	default:
		logger.Warn("websocket send buffer full, drop message", slog.String("user", c.User))
	}
}
