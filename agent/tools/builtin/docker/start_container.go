package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type StartContainerTool struct{ baseTool }

func (t *StartContainerTool) Name() string { return "docker_start_container" }

//go:embed docker_start_container.txt
var dockerStartContainerDescription string

func (t *StartContainerTool) Description() string {
	return dockerStartContainerDescription
}
func (t *StartContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
		},
		"required": []string{"container_id"},
	}
}
func (t *StartContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	if err := t.client.StartContainer(ctx, id); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"message": "started", "container_id": id}, nil
}
func (t *StartContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_start_container", IsFromToolkit: false, ToolType: "docker"}
}
