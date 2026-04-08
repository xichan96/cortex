package gateway

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/agent/types"
	dinopkg "github.com/xichan96/cortex/dino/pkg"
)

var (
	ErrSessionIDRequired = errors.New("session_id is required")
	ErrChatBodyEmpty     = errors.New("text or content required")
)

func ParseCreateSessionBody(c *gin.Context) (CreateSessionBody, error) {
	var req CreateSessionBody
	if err := c.ShouldBindJSON(&req); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return req, ErrSessionIDRequired
	}
	if err := dinopkg.ValidateSessionID(req.SessionID); err != nil {
		return req, err
	}
	return req, nil
}

func ParseChatBody(c *gin.Context, pathSessionID string) (ChatBody, types.Message, error) {
	var req ChatBody
	if err := c.ShouldBindJSON(&req); err != nil {
		return req, types.Message{}, err
	}
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		sid = strings.TrimSpace(pathSessionID)
	}
	req.SessionID = sid
	if err := dinopkg.ValidateSessionID(sid); err != nil {
		return req, types.Message{}, err
	}
	msg := dinopkg.BuildUserMessage(req.Content, req.Text, req.Parts, req.Attachments)
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return req, msg, ErrChatBodyEmpty
	}
	return req, msg, nil
}
