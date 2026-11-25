package engine

import "github.com/xichan96/cortex/agent/types"

// repairToolPairing normalizes history before a model request:
//   - every assistant tool_call gets exactly one tool result, immediately after
//     the assistant message and in call order;
//   - orphan tool results (no matching call, e.g. after a cancelled turn) are
//     dropped;
//   - a missing result becomes an explicit error observation.
//
// Valid histories are returned unchanged. This prevents malformed
// tool_use/tool_result pairs from poisoning every later request.
func repairToolPairing(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return messages
	}
	results := make(map[string]types.Message, len(messages))
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			if _, exists := results[m.ToolCallID]; !exists {
				results[m.ToolCallID] = m
			}
		}
	}

	out := make([]types.Message, 0, len(messages))
	used := make(map[string]bool, len(results))
	changed := false
	for _, m := range messages {
		if m.Role == "tool" {
			// Re-emitted after its assistant message below.
			changed = true
			continue
		}
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if r, ok := results[tc.ID]; ok && !used[tc.ID] {
				out = append(out, r)
				used[tc.ID] = true
				continue
			}
			changed = true
			out = append(out, types.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    "tool result missing (previous turn was interrupted)",
			})
		}
	}
	if !changed {
		return messages
	}
	return out
}
