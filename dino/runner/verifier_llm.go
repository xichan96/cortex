package runner

import (
	"context"
	"strings"

	agenttypes "github.com/xichan96/cortex/agent/types"
	dinotask "github.com/xichan96/cortex/dino/task"
	dinoverify "github.com/xichan96/cortex/dino/verify"
)

type LLMChatFunc func(ctx context.Context, messages []agenttypes.Message) (content string, err error)

type LLMVerifier struct {
	Chat         LLMChatFunc
	SystemPrompt string
}

func NewLLMVerifier(chat LLMChatFunc, systemPrompt string) *LLMVerifier {
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultLLMVerifySystemPrompt
	}
	return &LLMVerifier{Chat: chat, SystemPrompt: systemPrompt}
}

const defaultLLMVerifySystemPrompt = `You grade whether the assistant output satisfies the task. Reply with exactly one line starting with YES or NO (uppercase). If NO, add a short reason on the same line after a colon or on following lines.`

func (v *LLMVerifier) Verify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	if v == nil || v.Chat == nil {
		return false, "llm verifier: nil chat"
	}
	sys := v.SystemPrompt
	if tk != nil && tk.Config != nil && strings.TrimSpace(tk.Config.VerifyLLMSystemPrompt) != "" {
		sys = tk.Config.VerifyLLMSystemPrompt
	}
	var ub strings.Builder
	if tk != nil {
		ub.WriteString("Task description:\n")
		ub.WriteString(tk.Description)
		ub.WriteString("\n\n")
	}
	ub.WriteString("Assistant output:\n")
	if snap != nil {
		ub.WriteString(snap.AssistantText)
	}
	msgs := []agenttypes.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: ub.String()},
	}
	reply, err := v.Chat(ctx, msgs)
	if err != nil {
		return false, err.Error()
	}
	return dinoverify.ParseYesNoAnswer(reply)
}
