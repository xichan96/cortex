package builtin

import (
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type TimeTool struct{}

func NewTimeTool() types.Tool {
	return &TimeTool{}
}

func (t *TimeTool) Name() string {
	return "get_time"
}

func (t *TimeTool) Description() string {
	return "Get current time in specified timezone. Default timezone is Asia/Hong_Kong."
}

func (t *TimeTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "Timezone name (e.g., 'Asia/Hong_Kong', 'America/New_York', 'UTC'). Default is 'Asia/Hong_Kong'",
			},
		},
		"required": []string{},
	}
}

func (t *TimeTool) Execute(input map[string]interface{}) (interface{}, error) {
	timezone := "Asia/Hong_Kong"
	if tz, ok := input["timezone"].(string); ok && tz != "" {
		timezone = tz
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid timezone '%s': %v", timezone, err))
	}

	now := time.Now().In(loc)

	return map[string]interface{}{
		"time":      now.Format(time.RFC3339),
		"timezone":  timezone,
		"unix":      now.Unix(),
		"formatted": now.Format("2006-01-02 15:04:05 MST"),
	}, nil
}

func (t *TimeTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "time",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
