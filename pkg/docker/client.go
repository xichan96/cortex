package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

type ClientConfig struct {
	DockerConfig *DockerConfig `json:"docker"`
	VolImage     string        `json:"vol_image"`
}

type DockerConfig struct {
	Socket  string `json:"socket"`
	Port    int    `json:"port"`
	TLS     bool   `json:"tls"`
	TLSCa   string `json:"tls_ca"`
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`
}

// NewClient 创建 Docker 客户端
func NewClient(host string, cfg ClientConfig) (Client, error) {
	return NewDockerClient(host, cfg)
}

type Client interface {
	ContainerClient

	ImageClient

	NetworkClient

	Close() error
}

type ContainerClient interface {
	CreateContainer(ctx context.Context, r Container) (containerID string, err error)
	DeleteContainer(ctx context.Context, containerID string) error
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RestartContainer(ctx context.Context, containerID string) error
	LogContainer(ctx context.Context, log ContainerLog) (string, error)
	LogContainerStream(ctx context.Context, log ContainerLog) (chan string, error)
	TerminalContainer(ctx context.Context, tty *ContainerTerminal) (chan []byte, error)
	ExecContainer(ctx context.Context, containerID string, cmd string) ([]byte, error)
	InspectContainer(ctx context.Context, containerID string) (*types.ContainerJSON, error)
	ListContainer(ctx context.Context, opt container.ListOptions) ([]types.Container, error)
	CommitContainer(ctx context.Context, containerID string, imageName string) (string, error)
	InspectImage(ctx context.Context, id string) (types.ImageInspect, error)
}

type ImageClient interface {
	PullImage(ctx context.Context, image string, auth *ImageAuth) error
	PushImage(ctx context.Context, image string, auth *ImageAuth) error
}

type NetworkClient interface {
	CreateNetwork(ctx context.Context, name string, options network.CreateOptions) error
	GetNetwork(ctx context.Context, name string) error
}
