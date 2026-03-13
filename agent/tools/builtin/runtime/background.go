package runtime

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/shell"
)

var defaultBgManager = shell.NewBackgroundShellManager()

type JobKillTool struct{}

func NewJobKillTool() types.Tool {
	return &JobKillTool{}
}

func (t *JobKillTool) Name() string {
	return "job_kill"
}

//go:embed job_kill.txt
var jobKillDescription string

func (t *JobKillTool) Description() string {
	return jobKillDescription
}

func (t *JobKillTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"shell_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the background shell to terminate",
			},
		},
		"required": []string{"shell_id"},
	}
}

func (t *JobKillTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shellID, ok := input["shell_id"].(string)
	if !ok || shellID == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("shell_id is required"))
	}
	if err := defaultBgManager.Kill(shellID); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"status": "ok", "message": fmt.Sprintf("Background shell %s terminated", shellID)}, nil
}

func (t *JobKillTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "job_kill",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}

type JobOutputTool struct{}

func NewJobOutputTool() types.Tool {
	return &JobOutputTool{}
}

func (t *JobOutputTool) Name() string {
	return "job_output"
}

//go:embed job_output.txt
var jobOutputDescription string

func (t *JobOutputTool) Description() string {
	return jobOutputDescription
}

func (t *JobOutputTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"shell_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the background shell to retrieve output from",
			},
		},
		"required": []string{"shell_id"},
	}
}

func (t *JobOutputTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shellID, ok := input["shell_id"].(string)
	if !ok || shellID == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("shell_id is required"))
	}
	bs, ok := defaultBgManager.Get(shellID)
	if !ok {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("background shell not found: %s", shellID))
	}
	stdout, stderr, done, err := bs.GetOutput()
	var parts []string
	if stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr != "" {
		parts = append(parts, stderr)
	}
	status := "running"
	if done {
		status = "completed"
		if err != nil {
			if code := shell.ExitCode(err); code != 0 {
				parts = append(parts, fmt.Sprintf("Exit code %d", code))
			}
		}
	}
	output := strings.Join(parts, "\n")
	if output == "" {
		output = "no output"
	}
	return map[string]interface{}{
		"shell_id": shellID,
		"status":   status,
		"output":   output,
	}, nil
}

func (t *JobOutputTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "job_output",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}
