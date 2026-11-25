package engine

import (
	"context"

	"github.com/xichan96/cortex/agent/question"
	"github.com/xichan96/cortex/agent/types"
)

// hasQuestionSentinel reports whether the iteration's intermediate steps
// include a "question" tool call that must halt the turn.
//
// Legacy mode (no interactive asker on the turn context): the question tool
// returned SentinelQuestionResult and the answer comes back as a fresh user
// input, so continuing the iteration would only feed the sentinel back to the
// model (question-reflow design §2.1).
//
// Blocking mode: the session injected a question.Asker into the turn context;
// the tool already blocked and returned the user's answers as its tool result,
// so the iteration must continue normally.
//
// Detection uses the tool name (Action.Tool) rather than parsing the
// observation JSON (review B2/R2): the tool name is always present in
// ToolCallData regardless of truncation, so this is robust even when
// TruncateToolResult mid-cuts a long question.
func hasQuestionSentinel(ctx context.Context, steps []types.ToolCallData) bool {
	if _, blocking := question.AskerFromContext(ctx); blocking {
		return false
	}
	for _, step := range steps {
		if step.Action.Tool == "question" {
			return true
		}
	}
	return false
}
