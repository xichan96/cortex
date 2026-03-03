package docker

import (
	"context"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	dockerpkg "github.com/xichan96/cortex/pkg/docker"
	"github.com/xichan96/cortex/pkg/errors"
)

type ContainerLogsTool struct{ baseTool }

func (t *ContainerLogsTool) Name() string { return "docker_container_logs" }
func (t *ContainerLogsTool) Description() string {
	return "Get logs of a Docker container. Optional: lines (int), timestamps (bool)."
}
func (t *ContainerLogsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
			"lines":        map[string]interface{}{"type": "integer", "description": "Number of lines from end (default 100)"},
			"timestamps":   map[string]interface{}{"type": "boolean", "description": "Include timestamps"},
		},
		"required": []string{"container_id"},
	}
}
func (t *ContainerLogsTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	id, ok := input["container_id"].(string)
	if !ok || id == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("container_id is required"))
	}
	logOpt := dockerpkg.ContainerLog{ContainerID: id, Lines: 100, Follow: false}
	if v, ok := input["lines"].(float64); ok {
		logOpt.Lines = int(v)
	} else if v, ok := input["lines"].(int); ok {
		logOpt.Lines = v
	}
	if logOpt.Lines <= 0 {
		logOpt.Lines = 100
	}
	if v, ok := input["timestamps"].(bool); ok {
		logOpt.Timestamp = v
	}
	out, err := t.client.LogContainer(ctx, logOpt)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"logs": out}, nil
}
func (t *ContainerLogsTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_container_logs", IsFromToolkit: false, ToolType: "docker"}
}
