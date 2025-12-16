package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
)

type Handler interface {
	GetMessageRequest(c *gin.Context) (*MessageRequest, error)
	ChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest)
	StreamChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest)
}

type handler struct {
}

func NewHandler() Handler {
	return &handler{}
}

func (h *handler) handleError(err error) *errors.Error {
	if e, ok := err.(*errors.Error); ok {
		return e
	}
	return errors.EC_HTTP_EXECUTE_FAILED.Wrap(err)
}

func (h *handler) sendSSEvent(c *gin.Context, event SSEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		return false
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

func (h *handler) GetMessageRequest(c *gin.Context) (*MessageRequest, error) {
	var req MessageRequest
	if c.Request.Method == "POST" {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Status: errors.EC_HTTP_INVALID_REQUEST.Code,
				Msg:    errors.EC_HTTP_INVALID_REQUEST.Message,
			})
			return nil, errors.EC_HTTP_INVALID_REQUEST.Wrap(err)
		}
	} else {
		c.JSON(http.StatusMethodNotAllowed, ErrorResponse{
			Status: errors.EC_HTTP_INVALID_METHOD.Code,
			Msg:    errors.EC_HTTP_INVALID_METHOD.Message,
		})
		return nil, errors.EC_HTTP_INVALID_METHOD
	}
	return &req, nil
}

func (h *handler) ChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest) {
	result, err := engine.Execute(req.Message, nil)
	if err != nil {
		ec := h.handleError(err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Status: ec.Code,
			Msg:    ec.Message,
		})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *handler) StreamChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ctx := c.Request.Context()
	stream, err := engine.ExecuteStream(req.Message, nil)
	if err != nil {
		ec := h.handleError(err)
		if !h.sendSSEvent(c, SSEvent{
			Type:  "error",
			Error: fmt.Sprintf("%d: %s", ec.Code, ec.Message),
		}) {
			return
		}
		return
	}

	for result := range stream {
		select {
		case <-ctx.Done():
			return
		default:
			switch result.Type {
			case "chunk":
				if !h.sendSSEvent(c, SSEvent{
					Type:    "chunk",
					Content: result.Content,
				}) {
					return
				}
			case "error":
				var errorMsg string
				if result.Error != nil {
					if ec, ok := result.Error.(*errors.Error); ok {
						errorMsg = fmt.Sprintf("%d: %s", ec.Code, ec.Message)
					} else {
						errorMsg = result.Error.Error()
					}
				}
				if !h.sendSSEvent(c, SSEvent{
					Type:  "error",
					Error: errorMsg,
				}) {
					return
				}
			case "end":
				if !h.sendSSEvent(c, SSEvent{
					Type: "end",
					End:  true,
					Data: result.Result,
				}) {
					return
				}
			}
		}
	}
}
