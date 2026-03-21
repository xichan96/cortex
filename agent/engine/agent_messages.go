package engine

import (
	"github.com/xichan96/cortex/agent/types"
)

// buildNextMessages appends one assistant turn (with tool_calls) and following tool
// messages to the full previous transcript so the model retains prior tool outputs on
// the next iteration.
func (ae *AgentEngine) buildNextMessages(previousMessages []types.Message, result *types.AgentResult) []types.Message {
	if result == nil {
		return previousMessages
	}
	if result.Output == "" && len(result.IntermediateSteps) == 0 {
		return previousMessages
	}
	n := len(previousMessages)
	added := types.MessagesFromToolSteps(result.Output, result.IntermediateSteps)
	if len(added) == 0 {
		out := make([]types.Message, n, n+1)
		copy(out, previousMessages)
		return append(out, types.Message{Role: "assistant", Content: result.Output})
	}
	out := make([]types.Message, n, n+len(added))
	copy(out, previousMessages)
	return append(out, added...)
}
