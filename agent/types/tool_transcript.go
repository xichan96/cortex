package types

import (
	"encoding/json"
	"fmt"
)

func toolCallIDString(step ToolCallData) string {
	if s, ok := step.Action.ToolCallID.(string); ok {
		return s
	}
	return fmt.Sprint(step.Action.ToolCallID)
}

func toolCallIDAt(step ToolCallData, i int) string {
	id := toolCallIDString(step)
	if id == "" || id == "<nil>" {
		return fmt.Sprintf("cortex-step-%d", i)
	}
	return id
}

func toolCallTypeString(v interface{}) string {
	if v == nil {
		return "function"
	}
	s := fmt.Sprint(v)
	if s == "" || s == "<nil>" {
		return "function"
	}
	return s
}

func toolInputAsMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	if s, ok := v.(string); ok && s != "" {
		var m map[string]interface{}
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	return map[string]interface{}{}
}

// MessagesFromToolSteps builds assistant (with tool_calls) and tool messages for one model turn.
func MessagesFromToolSteps(assistantContent string, steps []ToolCallData) []Message {
	if len(steps) == 0 {
		return nil
	}
	out := make([]Message, 0, 1+len(steps))
	calls := make([]ToolCall, 0, len(steps))
	for i, step := range steps {
		id := toolCallIDAt(step, i)
		args := toolInputAsMap(step.Action.ToolInput)
		calls = append(calls, ToolCall{
			ID:   id,
			Type: toolCallTypeString(step.Action.Type),
			Function: ToolFunction{
				Name:      step.Action.Tool,
				Arguments: args,
			},
		})
	}
	out = append(out, Message{
		Role:      "assistant",
		Content:   assistantContent,
		ToolCalls: calls,
	})
	for i, step := range steps {
		out = append(out, Message{
			Role:       "tool",
			Content:    step.Observation,
			Name:       step.Action.Tool,
			ToolCallID: toolCallIDAt(step, i),
		})
	}
	return out
}

type messageToolPersist struct {
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// MarshalMessageToolPersist JSON for DB/redis; empty string if no tool fields.
func MarshalMessageToolPersist(m Message) (string, error) {
	if len(m.ToolCalls) == 0 && m.ToolCallID == "" && m.Name == "" {
		return "", nil
	}
	b, err := json.Marshal(messageToolPersist{
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ApplyMessageToolPersist restores ToolCalls and ToolCallID from storage.
func ApplyMessageToolPersist(m *Message, s string) error {
	if s == "" || m == nil {
		return nil
	}
	var x messageToolPersist
	if err := json.Unmarshal([]byte(s), &x); err != nil {
		return err
	}
	m.ToolCalls = x.ToolCalls
	m.ToolCallID = x.ToolCallID
	m.Name = x.Name
	return nil
}
