package types

import (
	"errors"
	"testing"
)

func TestStopCauseFromChatError(t *testing.T) {
	if StopCauseFromChatError(nil) != AgentStopCauseNone {
		t.Fatal()
	}
	if StopCauseFromChatError(errors.New("maximum context length exceeded")) != AgentStopCauseContextWindow {
		t.Fatal()
	}
	if StopCauseFromChatError(errors.New("random failure")) != AgentStopCauseNone {
		t.Fatal()
	}
}
