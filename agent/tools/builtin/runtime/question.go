package runtime

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// SentinelQuestionResult is the structured payload QuestionTool.Execute returns
// (option A of the F7 design): instead of hard-erroring, it reports that the
// question must be surfaced to the user and that the agent loop should not
// continue waiting on a result it cannot produce. dino's runner is expected to
// detect this shape and emit an EventTypeQuestion; until then the sentinel is
// at least not a fatal error.
type SentinelQuestionResult struct {
	Ok       bool   `json:"ok"`
	Question string `json:"question"`
	AskUser  bool   `json:"ask_user"`
}

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
	// F7 option A: return a structured sentinel instead of hard-erroring. The
	// runner is expected to surface this to the UI and pause; until a runner
	// handles it, the sentinel is still preferable to an error because it does
	// not terminate the iteration and carries the question for the model/UI.
	return SentinelQuestionResult{
		Ok:       true,
		Question: question,
		AskUser:  true,
	}, nil
}

func (t *QuestionTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "question",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}
