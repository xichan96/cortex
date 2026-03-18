package tools

import (
	"github.com/xichan96/cortex/agent/types"
)

// ToolEventSender is an interface to send tool events
type ToolEventSender interface {
	SendToolCall(sessionID, toolCallID, toolName string, input map[string]interface{})
	SendToolResult(sessionID, toolCallID, toolName string, result string)
	SendToolError(sessionID, toolCallID, toolName string, err string)
}

// ToolCallbackAdapter sends real-time tool events to a ToolEventSender
type ToolCallbackAdapter struct {
	SessionID string
	Sender    ToolEventSender
}

func (a *ToolCallbackAdapter) OnToolCall(toolName string, toolCallID string, input map[string]interface{}) {
	if a.Sender != nil {
		a.Sender.SendToolCall(a.SessionID, toolCallID, toolName, input)
	}
}

func (a *ToolCallbackAdapter) OnToolInputStart(toolName string, toolCallID string, input map[string]interface{}) {
}

func (a *ToolCallbackAdapter) OnToolInputEnd(toolName string, toolCallID string, input map[string]interface{}) {
}

func (a *ToolCallbackAdapter) OnToolResult(toolName string, toolCallID string, output interface{}) {
	if a.Sender != nil {
		a.Sender.SendToolResult(a.SessionID, toolCallID, toolName, types.FormatToolResult(output))
	}
}

func (a *ToolCallbackAdapter) OnToolError(toolName string, toolCallID string, err error) {
	if a.Sender != nil {
		a.Sender.SendToolError(a.SessionID, toolCallID, toolName, types.NormalizeToolError(err, 0))
	}
}
