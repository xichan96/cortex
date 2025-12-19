package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

type Handler interface {
	GetMessageRequest(c *gin.Context) (*MessageRequest, error)
	ChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest)
	StreamChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest)
}

type handler struct {
	logger *logger.Logger
}

func NewHandler() Handler {
	return &handler{
		logger: logger.NewLogger(),
	}
}

func (h *handler) handleError(err error) *errors.Error {
	if e, ok := err.(*errors.Error); ok {
		return e
	}
	return errors.EC_HTTP_EXECUTE_FAILED.Wrap(err)
}

func (h *handler) formatError(err error) string {
	if e, ok := err.(*errors.Error); ok {
		return fmt.Sprintf("%d: %s", e.Code, e.Message)
	}
	return err.Error()
}

func (h *handler) sendSSEvent(c *gin.Context, event SSEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		h.logger.LogError("sendSSEvent", err,
			slog.String("event_type", event.Type),
			slog.String("operation", "marshal"))
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		h.logger.LogError("sendSSEvent", err,
			slog.String("event_type", event.Type),
			slog.String("operation", "write"))
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
	if engine == nil {
		h.logger.LogError("ChatAPI", fmt.Errorf("agent engine is nil"))
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Status: errors.EC_HTTP_EXECUTE_FAILED.Code,
			Msg:    "agent engine is not available",
		})
		return
	}

	result, err := engine.Execute(req.Message, nil)
	if err != nil {
		ec := h.handleError(err)
		h.logger.LogError("ChatAPI", err,
			slog.String("session_id", req.SessionID),
			slog.Int("error_code", ec.Code))
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Status: ec.Code,
			Msg:    ec.Message,
		})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *handler) StreamChatAPI(c *gin.Context, engine *engine.AgentEngine, req *MessageRequest) {
	if engine == nil {
		h.logger.LogError("StreamChatAPI", fmt.Errorf("agent engine is nil"))
		c.Header("Content-Type", "text/event-stream")
		if !h.sendSSEvent(c, SSEvent{
			Type:  "error",
			Error: fmt.Sprintf("%d: %s", errors.EC_HTTP_EXECUTE_FAILED.Code, "agent engine is not available"),
		}) {
			return
		}
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ctx := c.Request.Context()
	stream, err := engine.ExecuteStream(req.Message, nil)
	if err != nil {
		ec := h.handleError(err)
		h.logger.LogError("StreamChatAPI", err,
			slog.String("session_id", req.SessionID),
			slog.Int("error_code", ec.Code))
		if !h.sendSSEvent(c, SSEvent{
			Type:  "error",
			Error: h.formatError(ec),
		}) {
			return
		}
		return
	}

	for result := range stream {
		select {
		case <-ctx.Done():
			h.logger.Info("Stream context cancelled",
				slog.String("session_id", req.SessionID),
				slog.String("reason", ctx.Err().Error()))
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
				errorMsg := ""
				if result.Error != nil {
					errorMsg = h.formatError(result.Error)
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
