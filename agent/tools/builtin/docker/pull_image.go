package docker

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	dockerpkg "github.com/xichan96/cortex/pkg/docker"
	"github.com/xichan96/cortex/pkg/errors"
)

type PullImageTool struct{ baseTool }

func (t *PullImageTool) Name() string { return "docker_pull_image" }

//go:embed docker_pull_image.txt
var dockerPullImageDescription string

func (t *PullImageTool) Description() string {
	return dockerPullImageDescription
}
func (t *PullImageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"image":    map[string]interface{}{"type": "string", "description": "Image name (e.g. alpine:latest)"},
			"username": map[string]interface{}{"type": "string", "description": "Registry username"},
			"password": map[string]interface{}{"type": "string", "description": "Registry password"},
		},
		"required": []string{"image"},
	}
}
func (t *PullImageTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	image, ok := input["image"].(string)
	if !ok || image == "" {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("image is required"))
	}
	var auth *dockerpkg.ImageAuth
	if u, ok := input["username"].(string); ok && u != "" {
		auth = &dockerpkg.ImageAuth{Username: u}
		if p, ok := input["password"].(string); ok {
			auth.Password = p
		}
	}
	if err := t.client.PullImage(ctx, image, auth); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	return map[string]interface{}{"message": "pulled", "image": image}, nil
}
func (t *PullImageTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: "docker_pull_image", IsFromToolkit: false, ToolType: "docker"}
}
