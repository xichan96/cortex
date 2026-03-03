package docker

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestDockerClient(t *testing.T) {
	host := os.Getenv("DOCKER_TEST_HOST")
	if host == "" {
		t.Skip("")
	}
	port := 23375
	if rawPort := os.Getenv("DOCKER_TEST_PORT"); rawPort != "" {
		if parsed, err := strconv.Atoi(rawPort); err == nil {
			port = parsed
		}
	}
	dk, err := NewDockerClient(host, ClientConfig{
		DockerConfig: &DockerConfig{
			Port: port,
			TLS:  false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ret, err := dk.ListContainer(context.Background(), container.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(ret)
	containerID := os.Getenv("DOCKER_TEST_CONTAINER_ID")
	if containerID == "" {
		t.Skip("")
	}
	if err := dk.StopContainer(context.Background(), containerID); err != nil {
		t.Fatal(err)
	}
	t.Log("stop container success")
}
