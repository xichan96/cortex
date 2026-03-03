package docker

import (
	"github.com/xichan96/cortex/agent/types"
	dockerpkg "github.com/xichan96/cortex/pkg/docker"
)

func NewDockerTools(client dockerpkg.Client) []types.Tool {
	b := baseTool{client: client}
	return []types.Tool{
		&ListContainersTool{b},
		&InspectContainerTool{b},
		&CreateContainerTool{b},
		&StartContainerTool{b},
		&StopContainerTool{b},
		&RestartContainerTool{b},
		&RemoveContainerTool{b},
		&ContainerLogsTool{b},
		&ExecContainerTool{b},
		&PullImageTool{b},
	}
}
