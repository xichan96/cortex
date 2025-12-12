package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
)

type ServerIer interface {
	ChatHandler(c *gin.Context)
	StreamChatHandler(c *gin.Context)
}

type Server struct {
	engine *engine.AgentEngine
}

func NewServer(engine *engine.AgentEngine) ServerIer {
	return &Server{
		engine: engine,
	}
}

// sendSSEvent sends an SSE event
func (s *Server) sendSSEvent(c *gin.Context, event SSEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to serialize SSE event: %v", err)
		return
	}

	// Write SSE formatted data
	fmt.Fprintf(c.Writer, "data: %s\n\n", data)

	// Flush buffer
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (s *Server) ChatHandler(c *gin.Context) {
	var req MessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status": errors.EC_HTTP_INVALID_REQUEST.Code,
			"msg":    errors.EC_HTTP_INVALID_REQUEST.Message,
		})
		return
	}

	result, err := s.engine.Execute(req.Message, nil)
	if err != nil {
		var ec *errors.Error
		if e, ok := err.(*errors.Error); ok {
			ec = e
		} else {
			ec = errors.EC_HTTP_EXECUTE_FAILED.Wrap(err)
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": ec.Code,
			"msg":    ec.Message,
		})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) StreamChatHandler(c *gin.Context) {
	var req MessageRequest
	if c.Request.Method == "GET" {
		req.Message = c.Query("message")
		if req.Message == "" {
			s.sendSSEvent(c, SSEvent{
				Type:  "error",
				Error: fmt.Sprintf("%d: %s", errors.EC_HTTP_MESSAGE_EMPTY.Code, errors.EC_HTTP_MESSAGE_EMPTY.Message),
			})
			return
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			s.sendSSEvent(c, SSEvent{
				Type:  "error",
				Error: fmt.Sprintf("%d: %s", errors.EC_HTTP_INVALID_REQUEST.Code, errors.EC_HTTP_INVALID_REQUEST.Message),
			})
			return
		}
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// Create context with cancellation support
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// Use channel to listen for client disconnection
	notify := c.Writer.CloseNotify()
	go func() {
		<-notify
		cancel()
	}()

	stream, err := s.engine.ExecuteStream(req.Message, nil)
	if err != nil {
		var ec *errors.Error
		if e, ok := err.(*errors.Error); ok {
			ec = e
		} else {
			ec = errors.EC_HTTP_STREAM_EXECUTE_FAILED.Wrap(err)
		}
		s.sendSSEvent(c, SSEvent{
			Type:  "error",
			Error: fmt.Sprintf("%d: %s", ec.Code, ec.Message),
		})
		return
	}

	// Process streaming results
	for result := range stream {
		select {
		case <-ctx.Done():
			return
		default:
			switch result.Type {
			case "chunk":
				s.sendSSEvent(c, SSEvent{
					Type:    "chunk",
					Content: result.Content,
				})
			case "error":
				var errorMsg string
				if result.Error != nil {
					if ec, ok := result.Error.(*errors.Error); ok {
						errorMsg = fmt.Sprintf("%d: %s", ec.Code, ec.Message)
					} else {
						errorMsg = result.Error.Error()
					}
				}
				s.sendSSEvent(c, SSEvent{
					Type:  "error",
					Error: errorMsg,
				})
			case "end":
				s.sendSSEvent(c, SSEvent{
					Type: "end",
					End:  true,
					Data: result.Result,
				})
			}
		}
	}
}
