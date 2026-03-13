package runtime

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type QuestionTool struct{}

func NewQuestionTool() types.Tool {
	return &QuestionTool{}
}

func (t *QuestionTool) Name() string {
	return "question"
}

//go:embed question.txt
var questionDescription string

func (t *QuestionTool) Description() string {
	return questionDescription
}

func (t *QuestionTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user.",
			},
		},
		"required": []string{"question"},
	}
}

// This tool is special. It's handled by the agent runner, not executed directly.
// The agent should see this tool and halt execution, passing the question to the UI.
// The Execute function here is a fallback and should indicate that it needs special handling.
func (t *QuestionTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	question, ok := input["question"].(string)
	if !ok || question == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("question is required"))
	}
	return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("question tool must be handled by the agent runner, not executed directly"))
}

func (t *QuestionTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "question",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}
