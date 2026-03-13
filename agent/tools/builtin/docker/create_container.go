package docker

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"

	"github.com/xichan96/cortex/agent/types"
	dockerpkg "github.com/xichan96/cortex/pkg/docker"
	"github.com/xichan96/cortex/pkg/errors"
)

var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type CreateContainerTool struct{ baseTool }

func (t *CreateContainerTool) Name() string { return "docker_create_container" }

//go:embed docker_create_container.txt
var dockerCreateContainerDescription string

func (t *CreateContainerTool) Description() string {
	return dockerCreateContainerDescription
}
func (t *CreateContainerTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":       map[string]interface{}{"type": "string", "description": "Container name"},
			"image":      map[string]interface{}{"type": "string", "description": "Image name"},
			"cmd":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Command and args"},
			"env":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Env vars e.g. KEY=value"},
			"ports":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Port mappings e.g. 8080:80"},
			"pull_image": map[string]interface{}{"type": "boolean", "description": "Pull image before create"},
		},
		"required": []string{"name", "image"},
	}
}
func (t *CreateContainerTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	name, ok := input["name"].(string)
	if !ok || name == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("name is required"))
	}
	if !containerNameRe.MatchString(name) {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("name must match [a-zA-Z0-9][a-zA-Z0-9_.-]*"))
	}
	image, ok := input["image"].(string)
	if !ok || image == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("image is required"))
	}
	r := dockerpkg.Container{
		Name:      name,
		Image:     image,
		PullImage: false,
	}
	if v, ok := input["pull_image"].(bool); ok && v {
		r.PullImage = true
	}
	if v, ok := input["cmd"].([]interface{}); ok {
		for _, c := range v {
			if s, ok := c.(string); ok {
				r.Command = append(r.Command, s)
			}
		}
	}
	if v, ok := input["env"].([]interface{}); ok {
		for _, e := range v {
			if s, ok := e.(string); ok {
				r.Environment = append(r.Environment, s)
			}
		}
	}
	if v, ok := input["ports"].([]interface{}); ok {
		for _, p := range v {
			if s, ok := p.(string); ok {
				parts := splitPort(s)
				if parts == nil {
					return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid port %q, need hostPort:containerPort", s))
				}
				r.Ports = append(r.Ports, parts[0]+":"+parts[1])
			}
		}
	}
	cid, err := t.client.CreateContainer(ctx, r)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"container_id": cid, "name": name}, nil
}
func (t *CreateContainerTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_create_container", IsFromToolkit: false, ToolType: "docker"}
}

func splitPort(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			a, b := s[:i], s[i+1:]
			if a != "" && b != "" {
				return []string{a, b}
			}
			return nil
		}
	}
	return nil
}
