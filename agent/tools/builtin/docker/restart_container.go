package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type RestartContainerTool struct{ baseTool }

func (t *RestartContainerTool) Name() string { return "docker_restart_container" }

//go:embed docker_restart_container.txt
var dockerRestartContainerDescription string

func (t *RestartContainerTool) Description() string {
	return dockerRestartContainerDescription
}
func (t *RestartContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
		},
		"required": []string{"container_id"},
	}
}
func (t *RestartContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	if err := t.client.RestartContainer(ctx, id); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"message": "restarted", "container_id": id}, nil
}
func (t *RestartContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_restart_container", IsFromToolkit: false, ToolType: "docker"}
}
