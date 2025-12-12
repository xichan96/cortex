package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
)

type Handler interface {
	ChatAPI(c *gin.Context)
	StreamChatAPI(c *gin.Context)
}

type handler struct {
	engine *engine.AgentEngine
}

func NewHandler(engine *engine.AgentEngine) Handler {
	return &handler{
		engine: engine,
	}
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

func (h *handler) ChatAPI(c *gin.Context) {
	var req MessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Status: errors.EC_HTTP_INVALID_REQUEST.Code,
			Msg:    errors.EC_HTTP_INVALID_REQUEST.Message,
		})
		return
	}

	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Status: errors.EC_HTTP_MESSAGE_EMPTY.Code,
			Msg:    errors.EC_HTTP_MESSAGE_EMPTY.Message,
		})
		return
	}

	result, err := h.engine.Execute(req.Message, nil)
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

func (h *handler) StreamChatAPI(c *gin.Context) {
	var req MessageRequest
	if c.Request.Method == "GET" {
		req.Message = strings.TrimSpace(c.Query("message"))
		if req.Message == "" {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Status: errors.EC_HTTP_MESSAGE_EMPTY.Code,
				Msg:    errors.EC_HTTP_MESSAGE_EMPTY.Message,
			})
			return
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Status: errors.EC_HTTP_INVALID_REQUEST.Code,
				Msg:    errors.EC_HTTP_INVALID_REQUEST.Message,
			})
			return
		}
		req.Message = strings.TrimSpace(req.Message)
		if req.Message == "" {
			c.JSON(http.StatusBadRequest, ErrorResponse{
				Status: errors.EC_HTTP_MESSAGE_EMPTY.Code,
				Msg:    errors.EC_HTTP_MESSAGE_EMPTY.Message,
			})
			return
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ctx := c.Request.Context()
	stream, err := h.engine.ExecuteStream(req.Message, nil)
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
