package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type StopContainerTool struct{ baseTool }

func (t *StopContainerTool) Name() string { return "docker_stop_container" }

//go:embed docker_stop_container.txt
var dockerStopContainerDescription string

func (t *StopContainerTool) Description() string {
	return dockerStopContainerDescription
}
func (t *StopContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
		},
		"required": []string{"container_id"},
	}
}
func (t *StopContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	if err := t.client.StopContainer(ctx, id); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"message": "stopped", "container_id": id}, nil
}
func (t *StopContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_stop_container", IsFromToolkit: false, ToolType: "docker"}
}
