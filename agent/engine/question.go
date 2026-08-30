package engine

import (
	"github.com/xichan96/cortex/agent/types"
)

// hasQuestionSentinel reports whether the iteration's intermediate steps
// include a "question" tool call whose observation is a question sentinel
// (ask_user=true). The question tool halts the current turn: the answer comes
// back as a fresh user input, so continuing the iteration would only feed the
// sentinel back to the model (question-reflow design §2.1).
//
// Detection uses the tool name (Action.Tool) rather than parsing the
// observation JSON (review B2/R2): the tool name is always present in
// ToolCallData regardless of truncation, so this is robust even when
// TruncateToolResult mid-cuts a long question.
func hasQuestionSentinel(steps []types.ToolCallData) bool {
	for _, step := range steps {
		if step.Action.Tool == "question" {
			return true
		}
	}
	return false
}
