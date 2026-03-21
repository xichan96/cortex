package runtime

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/shell"
)

type CommandTool struct {
	workspace string
}

func NewCommandTool(workspace string) types.Tool {
	return &CommandTool{
		workspace: workspace,
	}
}

func (t *CommandTool) Name() string {
	return "command"
}

//go:embed command.txt
var commandDescription string

func (t *CommandTool) Description() string {
	return commandDescription
}

func (t *CommandTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Optional cap on command timeout in seconds; if omitted, uses the engine tool deadline when set, otherwise 30s",
			},
			"background": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, run command in background; use job_output to get output, job_kill to terminate",
			},
		},
		"required": []string{"command"},
	}
}

func (t *CommandTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	command, ok := input["command"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'command' parameter: must be a string"))
	}
	if command == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'command' parameter cannot be empty"))
	}

	if background, _ := input["background"].(bool); background {
		bs, err := defaultBgManager.Start(ctx, t.workspace, nil, command)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
		}
		return map[string]interface{}{
			"shell_id": bs.ID,
			"message":  "Started background command. Use job_output to check output, job_kill to terminate.",
		}, nil
	}

	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 {
			timeout = rem
		}
	}
	if timeoutVal, ok := input["timeout"].(float64); ok && timeoutVal > 0 {
		ut := time.Duration(timeoutVal) * time.Second
		if ut < timeout {
			timeout = ut
		}
	} else if timeoutVal, ok := input["timeout"].(int); ok && timeoutVal > 0 {
		ut := time.Duration(timeoutVal) * time.Second
		if ut < timeout {
			timeout = ut
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sh := shell.NewShell(&shell.Options{
		WorkingDir: t.workspace,
		Env:        shell.EnvironNonInteractive(),
	})
	stdout, stderr, err := sh.Exec(execCtx, command)

	if execCtx.Err() == context.DeadlineExceeded {
		return nil, errors.EC_TOOL_EXECUTION_TIMEOUT.Wrap(fmt.Errorf("command execution timeout after %v", timeout))
	}

	if err != nil {
		return map[string]interface{}{
			"command":   command,
			"exit_code": shell.ExitCode(err),
			"stdout":    stdout,
			"stderr":    stderr,
			"error":     err.Error(),
		}, nil
	}

	return map[string]interface{}{
		"command":   command,
		"exit_code": 0,
		"stdout":    stdout,
		"stderr":    stderr,
	}, nil
}

func (t *CommandTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "command",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}
