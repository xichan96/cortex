package pkg

import (
	"strings"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

var userEchoMu sync.Mutex
var pendingUserEcho = map[string]string{}
var awaitingFirstAssistantText = map[string]struct{}{}

func MarkPendingUserEcho(sessionID string, msg types.Message) {
	userEchoMu.Lock()
	defer userEchoMu.Unlock()
	ai := types.NewAgentInput(msg.Content)
	ai.Parts = msg.Parts
	pendingUserEcho[sessionID] = strings.TrimSpace(ai.String())
	awaitingFirstAssistantText[sessionID] = struct{}{}
}

func TakePendingUserEchoMatch(sessionID, content string) bool {
	userEchoMu.Lock()
	defer userEchoMu.Unlock()
	if _, ok := awaitingFirstAssistantText[sessionID]; !ok {
		return false
	}
	delete(awaitingFirstAssistantText, sessionID)
	want := pendingUserEcho[sessionID]
	delete(pendingUserEcho, sessionID)
	return strings.TrimSpace(content) == want
}
