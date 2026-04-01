package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

type ChatMessageSaver interface {
	AddMessage(ctx context.Context, msg types.Message) error
}

func coerceIntermediateSteps(raw interface{}) ([]types.ToolCallData, bool) {
	if raw == nil {
		return nil, true
	}
	if s, ok := raw.([]types.ToolCallData); ok {
		return s, true
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var out []types.ToolCallData
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}

func SaveOutputWithToolSteps(ctx context.Context, save ChatMessageSaver, output map[string]interface{}) error {
	outputMsg, ok := output["output"].(string)
	if !ok {
		return nil
	}
	role, _ := output["role"].(string)
	if role == "" {
		role = "assistant"
	}
	raw := output["intermediate_steps"]
	steps, decodeOK := coerceIntermediateSteps(raw)
	if !decodeOK && raw != nil {
		logger.LogError("SaveOutputWithToolSteps", fmt.Errorf("intermediate_steps: cannot decode to []ToolCallData"), slog.String("phase", "coerce_steps"))
	}
	if len(steps) > 0 {
		for _, m := range types.MessagesFromToolSteps(outputMsg, steps) {
			if err := save.AddMessage(ctx, m); err != nil {
				return err
			}
		}
		return nil
	}
	return save.AddMessage(ctx, types.Message{Role: role, Content: outputMsg})
}
