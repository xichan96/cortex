package builtin

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type CommandTool struct{}

func NewCommandTool() types.Tool {
	return &CommandTool{}
}

func (t *CommandTool) Name() string {
	return "command"
}

func (t *CommandTool) Description() string {
	return "Execute a shell command locally and return the output. Supports timeout configuration for long-running commands."
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
				"description": "Command execution timeout in seconds (default: 30)",
			},
		},
		"required": []string{"command"},
	}
}

func (t *CommandTool) Execute(input map[string]interface{}) (interface{}, error) {
	command, ok := input["command"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'command' parameter: must be a string"))
	}
	if command == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'command' parameter cannot be empty"))
	}

	timeout := 30 * time.Second
	if timeoutVal, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(timeoutVal) * time.Second
	} else if timeoutVal, ok := input["timeout"].(int); ok {
		timeout = time.Duration(timeoutVal) * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'command' parameter: command cannot be empty"))
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	errOutput := stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.EC_TOOL_EXECUTION_TIMEOUT.Wrap(fmt.Errorf("command execution timeout after %v", timeout))
	}

	if err != nil {
		return map[string]interface{}{
			"command":   command,
			"exit_code": cmd.ProcessState.ExitCode(),
			"stdout":    output,
			"stderr":    errOutput,
			"error":     err.Error(),
		}, nil
	}

	return map[string]interface{}{
		"command":   command,
		"exit_code": 0,
		"stdout":    output,
		"stderr":    errOutput,
	}, nil
}

func (t *CommandTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "command",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
