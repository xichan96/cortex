package task

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type TodoTool struct{}

func NewTodoTool() types.Tool {
	return &TodoTool{}
}

func (t *TodoTool) Name() string {
	return "todo"
}

//go:embed todo.txt
var todoDescription string

func (t *TodoTool) Description() string {
	return todoDescription
}

func (t *TodoTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "The operation to perform: 'add', 'remove', 'update'.",
				"enum": []string{"add", "remove", "update"},
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the task to remove or update.",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The content of the task to add or update.",
			},
		},
		"required": []string{"operation"},
	}
}

// This is another tool that might have special handling in the agent runner
// to update the UI.
func (t *TodoTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	// Placeholder implementation
	operation, ok := input["operation"].(string)
	if !ok || operation == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("operation is required"))
	}
	return fmt.Sprintf("Todo tool (%s) is not yet implemented.", operation), nil
}

func (t *TodoTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "todo",
		IsFromToolkit:  false,
		ToolType:       "task",
	}
}
