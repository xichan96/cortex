package docker

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/docker/go-units"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	ec "github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

const (
	defaultRuntime = "runc"
	hostMountPath  = "/host"
)

// implement ContainerClient interface
type DockerClient struct {
	cfg    *ClientConfig
	docker *client.Client
}

func NewDockerClient(host string, cfg ClientConfig) (Client, error) {
	var addr string
	if len(cfg.DockerConfig.Socket) > 0 {
		addr = fmt.Sprintf("unix://%s", cfg.DockerConfig.Socket)
	} else {
		addr = fmt.Sprintf("tcp://%s:%d", host, cfg.DockerConfig.Port)
	}
	ops := []client.Opt{
		client.WithAPIVersionNegotiation(),
		client.WithHost(addr),
	}
	if cfg.DockerConfig.TLS {
		ops = append(ops, withTLS(cfg.DockerConfig.TLSCa, cfg.DockerConfig.TLSCert, cfg.DockerConfig.TLSKey))
	}
	docker, err := client.NewClientWithOpts(ops...)
	if err != nil {
		return nil, err
	}
	d := newDockerClient(docker)
	d.cfg = &cfg
	return d, nil
}

func newDockerClient(docker *client.Client) *DockerClient {
	return &DockerClient{docker: docker}
}

func withTLS(caPemBs, certBs, keyBs string) client.Opt {
	return func(c *client.Client) error {
		hClient := c.HTTPClient()
		caPem, _ := base64.StdEncoding.DecodeString(caPemBs)
		cert, _ := base64.StdEncoding.DecodeString(certBs)
		key, _ := base64.StdEncoding.DecodeString(keyBs)
		cfg, err := tlsConfig(caPem, cert, key)
		if err != nil {
			logger.Error("create tls config error", slog.Any("error", err))
			return err
		}
		hClient.Transport.(*http.Transport).TLSClientConfig = cfg
		return nil
	}
}

func tlsConfig(caCertPEM, certPEM, keyPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	// create a CA certificate pool and add our CA certificate
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCertPEM); !ok {
		return nil, err
	}

	// configure TLS
	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true, // if not verify server certificate, set to true, not recommended
	}
	return tlsConfig, nil
}

func (d *DockerClient) InspectContainer(ctx context.Context, containerID string) (*types.ContainerJSON, error) {
	info, err := d.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	return &info, err
}

func (d *DockerClient) ListContainer(ctx context.Context, opt container.ListOptions) ([]types.Container, error) {
	containers, err := d.docker.ContainerList(ctx, opt)
	if err != nil {
		return nil, err
	}
	return containers, nil
}

func (d *DockerClient) CreateContainer(ctx context.Context, r Container) (containerID string, err error) {
	if r.PullImage {
		if err := d.PullImage(ctx, r.Image, r.ImageAuth); err != nil {
			return "", err
		}
	}
	if len(r.Runtime) == 0 {
		r.Runtime = defaultRuntime // default to runc
	}

	mm := make([]mount.Mount, 0, len(r.Volumes))
	for _, v := range r.Volumes {
		m := mount.Mount{
			Type:     mount.Type(v.Type),
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}
		if v.Shared {
			m.BindOptions = &mount.BindOptions{
				Propagation: mount.PropagationRShared, // 设置共享模式
			}
		}
		mm = append(mm, m)
	}

	containerCfg := container.Config{
		Env:          r.Environment,
		Entrypoint:   r.EntryPoint,
		Cmd:          r.Command,
		Image:        r.Image,
		WorkingDir:   r.WorkDir,
		User:         r.User,
		OpenStdin:    true,
		Tty:          true,
		ExposedPorts: make(nat.PortSet),
		Labels:       make(map[string]string, 0),
	}

	for key, value := range r.Labels {
		containerCfg.Labels[key] = value
	}

	pm := nat.PortMap{}
	for _, port := range r.Ports {
		ps := strings.Split(port, ":")

		p, err := nat.NewPort("tcp", ps[1])
		if err != nil {
			return "", err
		}
		pm[p] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: ps[0],
			},
		}
		containerCfg.ExposedPorts[p] = struct{}{}
	}

	restartPolicy := container.RestartPolicy{
		Name: container.RestartPolicyMode("unless-stopped"),
	}
	if len(r.Restart) != 0 {
		restartPolicy = container.RestartPolicy{
			Name: container.RestartPolicyMode(r.Restart),
		}
	}

	hostConfig := container.HostConfig{
		PortBindings:  pm,
		RestartPolicy: restartPolicy,
		Privileged:    r.Privileged,
		ShmSize:       int64(r.ShmSize) * units.GiB,
		Resources: container.Resources{
			CPUShares: 1024,
			Memory:    int64(r.Memory) * units.GiB,
			NanoCPUs:  int64(r.CPU * 1e9),
		},
		Mounts:     mm,
		Links:      r.Links,
		Runtime:    r.Runtime,
		AutoRemove: r.IsRemove,
		ExtraHosts: r.Hosts,
	}
	if r.HostNetwork {
		hostConfig.NetworkMode = "host"
	}
	if r.HostIpcMode {
		hostConfig.IpcMode = "host"
	}

	networkingConfig := network.NetworkingConfig{}
	if len(r.NetworkName) != 0 {
		networkingConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			r.NetworkName: {
				Aliases: []string{r.Name},
			},
		}
	}
	platform := ocispec.Platform{}

	resp, err := d.docker.ContainerCreate(ctx, &containerCfg, &hostConfig, &networkingConfig, &platform, r.Name)
	if err != nil {
		logger.Error("ContainerCreate failed", slog.Any("error", err))
		return "", err
	}
	if err := d.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// StartContainer start container
func (d *DockerClient) StartContainer(ctx context.Context, containerID string) error {
	if err := d.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return err
	}
	return nil
}

// StopContainer stop container
func (d *DockerClient) StopContainer(ctx context.Context, containerID string) error {
	// check container state, if already stopped, return directly
	info, err := d.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}
	if !info.State.Running {
		// container already stopped, return successfully
		return nil
	}

	// use shorter timeout (10 seconds) to try normal stop
	timeout := 10
	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	err = d.docker.ContainerStop(stopCtx, containerID, container.StopOptions{Timeout: &timeout})
	if err == nil {
		// normal stop succeeded, verify container state
		info, checkErr := d.docker.ContainerInspect(ctx, containerID)
		if checkErr == nil && !info.State.Running {
			return nil
		}
		// if check failed but stop call succeeded, maybe container is stopping, wait and check again
		time.Sleep(500 * time.Millisecond)
		info, checkErr = d.docker.ContainerInspect(ctx, containerID)
		if checkErr == nil && !info.State.Running {
			return nil
		}
		// if still running, continue to force kill
		logger.Warn("Container stop command succeeded but container still running, forcing kill", slog.String("containerID", containerID))
	} else {
		// normal stop failed, use force kill
		logger.Warn("Container graceful stop failed, forcing kill", slog.String("containerID", containerID), slog.Any("error", err))
	}

	// use force kill
	killCtx, killCancel := context.WithTimeout(ctx, 5*time.Second)
	defer killCancel()

	if killErr := d.docker.ContainerKill(killCtx, containerID, "SIGKILL"); killErr != nil {
		// if force kill also failed, return original error
		if err != nil {
			return fmt.Errorf("stop failed: %v, kill failed: %v", err, killErr)
		}
		return fmt.Errorf("kill failed: %v", killErr)
	}

	// force kill succeeded, verify container state
	info, err = d.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}
	if !info.State.Running {
		return nil
	}

	return fmt.Errorf("container %s still running after stop and kill", containerID)
}

// RestartContainer restart container
func (d *DockerClient) RestartContainer(ctx context.Context, containerID string) error {
	timeout := 30
	if err := d.docker.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return err
	}
	return nil
}

// DeleteContainer delete container
func (d *DockerClient) DeleteContainer(ctx context.Context, containerID string) error {
	timeout := 30
	if err := d.docker.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return err
	}
	if err := d.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
		return err
	}
	return nil
}

func (d *DockerClient) CreateNetwork(ctx context.Context, name string, options network.CreateOptions) error {
	if _, err := d.docker.NetworkCreate(ctx, name, options); err != nil {
		return err
	}
	return nil
}

func (d *DockerClient) GetNetwork(ctx context.Context, name string) error {
	netList, err := d.docker.NetworkList(ctx, network.ListOptions{})

	if err != nil {
		return err
	}
	for _, net := range netList {
		if net.Name == name {
			return nil
		}
	}
	return ec.EC_DATA_NOT_FOUND
}

func (d *DockerClient) LogContainer(ctx context.Context, log ContainerLog) (string, error) {
	out, err := d.docker.ContainerLogs(ctx, log.ContainerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: log.Timestamp,
		Follow:     false,
		Tail:       strconv.Itoa(log.Lines),
	})
	if err != nil {
		return "", err
	}
	defer out.Close()

	var buf strings.Builder
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > 8 {
			buf.Write(line[8:])
			buf.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (d *DockerClient) LogContainerStream(ctx context.Context, log ContainerLog) (chan string, error) {
	out, err := d.docker.ContainerLogs(ctx, log.ContainerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: log.Timestamp,
		Follow:     log.Follow,
		Tail:       strconv.Itoa(log.Lines),
	})
	if err != nil {
		return nil, err
	}

	ret := make(chan string, 100)

	go func() {
		defer out.Close()
		defer close(ret)
		reader := bufio.NewReader(out)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						return
					}
					logger.Error("read container log error", slog.Any("error", err))
				}
				ret <- line
			}
		}
	}()
	return ret, nil
}

func (d *DockerClient) TerminalContainer(ctx context.Context, tty *ContainerTerminal) (chan []byte, error) {
	exportShell := fmt.Sprintf("TERM=xterm-256color; export TERM;  COLUMNS=%d; export COLUMNS;LINES=%d;export LINES;", tty.Cols, tty.Rows)
	cmdShell := "[ -x /bin/bash ] && ([ -x /usr/bin/script ] && /usr/bin/script -q -c \"/bin/bash\" /dev/null || exec /bin/bash) || exec /bin/sh"
	for _, env := range tty.Env {
		exportShell += fmt.Sprintf("export %s;", env)
	}
	if len(tty.Cmd) != 0 {
		cmdShell = tty.Cmd
	}

	sh := exportShell + cmdShell
	cmd := strslice.StrSlice([]string{"/bin/sh", "-c", sh}) // 你希望执行的命令
	// create exec instance
	resp, err := d.docker.ContainerExecCreate(ctx, tty.ContainerID, container.ExecOptions{
		Cmd:          cmd,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}

	execID := resp.ID

	// get exec connection
	hijackResp, err := d.docker.ContainerExecAttach(ctx, execID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	// start two goroutines, one for input, one for output

	go func() {
		defer hijackResp.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-tty.Input:
				if data == nil && !ok {
					return
				}

				_, err := hijackResp.Conn.Write(data)
				if err != nil {
					return
				}
			}
		}
	}()
	outputChan := make(chan []byte, 100)

	// handle standard output and standard error
	go func() {
		defer close(outputChan)
		defer d.Close()

		for {
			buf := make([]byte, 1024)
			n, err := hijackResp.Reader.Read(buf)
			if n > 0 {
				outputChan <- buf[:n]
			}
			if err != nil {
				if err != io.EOF {
					logger.Error("exec container error", slog.Any("error", err))
				}
				break
			}
		}
	}()

	return outputChan, nil
}

func (d *DockerClient) ExecContainer(ctx context.Context, containerID string, cmd string) ([]byte, error) {
	// create exec instance

	cmds := strslice.StrSlice([]string{"/bin/sh", "-c", cmd})
	resp, err := d.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmds,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	defer d.Close()

	execID := resp.ID

	// get exec connection
	hijackResp, err := d.docker.ContainerExecAttach(ctx, execID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, err
	}

	defer hijackResp.Close()

	msg := make([]byte, 0)

	for {
		message := make([]byte, 1024)
		n, err := hijackResp.Reader.Read(message)
		if n > 0 {
			msg = append(msg, message[:n]...)
		}
		if err != nil {
			if err != io.EOF {
				logger.Error("exec container error", slog.Any("error", err))
			}
			break
		}
	}
	return msg, nil
}

// CommitContainer save container as image
func (d *DockerClient) CommitContainer(ctx context.Context, containerID, imageName string) (id string, err error) {
	res, err := d.docker.ContainerCommit(ctx, containerID, container.CommitOptions{
		Reference: imageName,
	})
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func (d *DockerClient) registryAuth(auth *ImageAuth) string {
	if auth == nil || len(auth.Username) == 0 || len(auth.Password) == 0 {
		return ""
	}
	authConfig := map[string]string{
		"username": auth.Username,
		"password": auth.Password,
	}
	authConfigBytes, _ := json.Marshal(authConfig)
	return base64.URLEncoding.EncodeToString(authConfigBytes)
}

// PullImage pull image
func (d *DockerClient) PullImage(ctx context.Context, imageName string, auth *ImageAuth) error {
	reader, err := d.docker.ImagePull(ctx, imageName, image.PullOptions{
		RegistryAuth: d.registryAuth(auth),
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(io.Discard, reader)
	return err
}

// PushImage push image
func (d *DockerClient) PushImage(ctx context.Context, imageName string, auth *ImageAuth) error {
	reader, err := d.docker.ImagePush(ctx, imageName, image.PushOptions{
		RegistryAuth: d.registryAuth(auth),
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		// filter logic: ignore lines containing "keyword"
		if !strings.Contains(line, "progress") {
			logger.Info(line) // output lines not containing keyword
		}

		if strings.Contains(line, "error") {
			return ec.NewError(7001, "commit container error")
		}
	}

	return nil
}

func (d *DockerClient) InspectImage(ctx context.Context, id string) (types.ImageInspect, error) {
	res, _, err := d.docker.ImageInspectWithRaw(ctx, id)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return types.ImageInspect{}, ec.EC_DATA_NOT_FOUND
		}
		return types.ImageInspect{}, err
	}
	return res, nil
}

func (d *DockerClient) Close() error {
	return d.docker.Close()
}
