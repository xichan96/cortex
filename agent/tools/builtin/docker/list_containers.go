package docker

import (
	"context"
	"strings"

	dt "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type ListContainersTool struct{ baseTool }

func (t *ListContainersTool) Name() string { return "docker_list_containers" }
func (t *ListContainersTool) Description() string {
	return "List Docker containers. Optional: all (bool) to include stopped."
}
func (t *ListContainersTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"all": map[string]interface{}{"type": "boolean", "description": "Include stopped containers"},
		},
		"required": []string{},
	}
}
func (t *ListContainersTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	opt := container.ListOptions{}
	if v, _ := input["all"].(bool); v {
		opt.All = true
	}
	list, err := t.client.ListContainer(ctx, opt)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return formatContainers(list), nil
}
func (t *ListContainersTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_list_containers", IsFromToolkit: false, ToolType: "docker"}
}

func formatContainers(list []dt.Container) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, c := range list {
		names := make([]string, 0, len(c.Names))
		for _, n := range c.Names {
			names = append(names, strings.TrimPrefix(n, "/"))
		}
		out = append(out, map[string]interface{}{
			"id":     c.ID,
			"names":  names,
			"image":  c.Image,
			"state":  c.State,
			"status": c.Status,
		})
	}
	return out
}
