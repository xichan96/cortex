package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type ExecContainerTool struct{ baseTool }

func (t *ExecContainerTool) Name() string { return "docker_exec" }

//go:embed docker_exec.txt
var dockerExecDescription string

func (t *ExecContainerTool) Description() string {
	return dockerExecDescription
}
func (t *ExecContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
			"command":      map[string]interface{}{"type": "string", "description": "Shell command to run (e.g. /bin/sh -c 'echo hi')"},
		},
		"required": []string{"container_id", "command"},
	}
}
func (t *ExecContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	cmd, ok := input["command"].(string)
	if !ok || cmd == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("command is required"))
	}
	out, err := t.client.ExecContainer(ctx, id, cmd)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"output": string(out)}, nil
}
func (t *ExecContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_exec", IsFromToolkit: false, ToolType: "docker"}
}
