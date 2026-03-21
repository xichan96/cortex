package engine

import (
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

func toolCallIDString(step types.ToolCallData) string {
	if s, ok := step.Action.ToolCallID.(string); ok {
		return s
	}
	return fmt.Sprint(step.Action.ToolCallID)
}

func (ae *AgentEngine) buildContextFromPreviousRequests(requests []types.ToolCallData) string {
	var builder strings.Builder
	builder.Grow(256 * len(requests))
	builder.WriteString("Previous tool calls:\n")
	for _, req := range requests {
		builder.WriteString(fmt.Sprintf("Tool: %s, Input: %v, Result: %s\n",
			req.Action.Tool, req.Action.ToolInput, req.Observation))
	}
	return builder.String()
}

// buildNextMessages appends one assistant turn (with tool_calls) and following tool
// messages to the full previous transcript so the model retains prior tool outputs on
// the next iteration.
func (ae *AgentEngine) buildNextMessages(previousMessages []types.Message, result *types.AgentResult) []types.Message {
	if result == nil || (result.Output == "" && len(result.IntermediateSteps) == 0) {
		return previousMessages
	}

	n := len(previousMessages)
	capacity := n + 1 + len(result.IntermediateSteps)
	out := make([]types.Message, n, capacity)
	copy(out, previousMessages)

	assistantToolCalls := make([]types.ToolCall, 0, len(result.IntermediateSteps))
	for _, step := range result.IntermediateSteps {
		args, _ := step.Action.ToolInput.(map[string]interface{})
		if args == nil {
			args = make(map[string]interface{})
		}
		assistantToolCalls = append(assistantToolCalls, types.ToolCall{
			ID:   toolCallIDString(step),
			Type: fmt.Sprint(step.Action.Type),
			Function: types.ToolFunction{
				Name:      step.Action.Tool,
				Arguments: args,
			},
		})
	}
	out = append(out, types.Message{
		Role:      "assistant",
		Content:   result.Output,
		ToolCalls: assistantToolCalls,
	})

	for _, step := range result.IntermediateSteps {
		out = append(out, types.Message{
			Role:       "tool",
			Content:    step.Observation,
			ToolCallID: toolCallIDString(step),
		})
	}

	return out
}
