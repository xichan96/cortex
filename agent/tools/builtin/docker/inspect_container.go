package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type InspectContainerTool struct{ baseTool }

func (t *InspectContainerTool) Name() string { return "docker_inspect_container" }

//go:embed docker_inspect_container.txt
var dockerInspectContainerDescription string

func (t *InspectContainerTool) Description() string {
	return dockerInspectContainerDescription
}
func (t *InspectContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
		},
		"required": []string{"container_id"},
	}
}
func (t *InspectContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	info, err := t.client.InspectContainer(ctx, id)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{
		"id":      info.ID,
		"name":    info.Name,
		"state":   info.State.Status,
		"running": info.State.Running,
		"image":   info.Config.Image,
	}, nil
}
func (t *InspectContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_inspect_container", IsFromToolkit: false, ToolType: "docker"}
}
