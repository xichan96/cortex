package types

import (
	"strings"
)

type AgentStopCause string

const (
	AgentStopCauseNone          AgentStopCause = ""
	AgentStopCauseMaxIterations AgentStopCause = "max_iterations"
	AgentStopCauseContextWindow AgentStopCause = "context_window"
	AgentStopCauseDoomLoop      AgentStopCause = "doom_loop"
)

func StopCauseFromChatError(err error) AgentStopCause {
	if err == nil {
		return AgentStopCauseNone
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "context length") || strings.Contains(s, "context window") {
		return AgentStopCauseContextWindow
	}
	if strings.Contains(s, "maximum context") {
		return AgentStopCauseContextWindow
	}
	if strings.Contains(s, "context_window_exceeded") || (strings.Contains(s, "token") && strings.Contains(s, "limit")) {
		return AgentStopCauseContextWindow
	}
	if strings.Contains(s, "too many tokens") || strings.Contains(s, "prompt is too long") {
		return AgentStopCauseContextWindow
	}
	return AgentStopCauseNone
}
