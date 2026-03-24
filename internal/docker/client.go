package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	ntypes "github.com/tijn/nodetester/pkg/types"
)

// Client wraps the Docker API client with our domain operations.
type Client struct {
	api *client.Client
}

// New creates a Docker client using the default environment settings
// (DOCKER_HOST, etc.). This connects to the real Docker daemon.
func New() (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &Client{api: cli}, nil
}

// Close releases the underlying Docker client resources.
func (c *Client) Close() error {
	return c.api.Close()
}

// PullImage pulls the specified image if not already present locally.
func (c *Client) PullImage(ctx context.Context, imageName string) error {
	reader, err := c.api.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", imageName, err)
	}
	defer reader.Close()
	// Drain the pull output (required to complete the pull).
	_, _ = io.Copy(os.Stdout, reader)
	return nil
}

// CreateNetwork creates a bridge network for the scenario.
// Returns the network ID.
func (c *Client) CreateNetwork(ctx context.Context, name string) (string, error) {
	resp, err := c.api.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return "", fmt.Errorf("creating network %s: %w", name, err)
	}
	return resp.ID, nil
}

// RemoveNetwork removes a network by name.
func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	return c.api.NetworkRemove(ctx, name)
}

// CreateNode creates and starts a single container attached to the given network.
func (c *Client) CreateNode(ctx context.Context, name, networkName string, spec ntypes.NodeSpec) (string, error) {
	// Build environment variable list.
	var env []string
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	// Use the specified command, or default to sleeping so the container stays alive.
	cmd := spec.Command
	if len(cmd) == 0 {
		cmd = []string{"sleep", "infinity"}
	}

	containerCfg := &container.Config{
		Image: spec.Image,
		Cmd:   cmd,
		Env:   env,
		Labels: map[string]string{
			"nodetester": "true", // label so we can find our containers later
		},
	}

	// NET_ADMIN is required for tc/netem latency injection.
	hostCfg := &container.HostConfig{
		CapAdd: []string{"NET_ADMIN"},
	}

	networkCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := c.api.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("creating container %s: %w", name, err)
	}

	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("starting container %s: %w", name, err)
	}

	return resp.ID, nil
}

// StopNode stops a running container.
func (c *Client) StopNode(ctx context.Context, name string) error {
	return c.api.ContainerStop(ctx, name, container.StopOptions{})
}

// RestartNode restarts a container.
func (c *Client) RestartNode(ctx context.Context, name string) error {
	return c.api.ContainerRestart(ctx, name, container.StopOptions{})
}

// RemoveNode force-removes a container.
func (c *Client) RemoveNode(ctx context.Context, name string) error {
	return c.api.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
}

// InspectNode returns the status of a container by name.
func (c *Client) InspectNode(ctx context.Context, name string) (*ntypes.NodeStatus, error) {
	info, err := c.api.ContainerInspect(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("inspecting container %s: %w", name, err)
	}
	return &ntypes.NodeStatus{
		Name:         name,
		ContainerID:  info.ID[:12],
		State:        info.State.Status,
		RestartCount: info.RestartCount,
	}, nil
}

// ListNodes returns all containers with the nodetester label.
func (c *Client) ListNodes(ctx context.Context) ([]ntypes.NodeStatus, error) {
	containers, err := c.api.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var nodes []ntypes.NodeStatus
	for _, ctr := range containers {
		if _, ok := ctr.Labels["nodetester"]; !ok {
			continue
		}
		name := ""
		if len(ctr.Names) > 0 {
			name = ctr.Names[0][1:] // Docker prefixes names with "/"
		}
		nodes = append(nodes, ntypes.NodeStatus{
			Name:        name,
			ContainerID: ctr.ID[:12],
			State:       ctr.State,
		})
	}
	return nodes, nil
}

// CopyToContainer writes in-memory content as a file inside a container.
// destDir is the target directory (e.g. "/data"), fileName is the file name.
// The Docker API requires a tar stream, so we build one in memory.
func (c *Client) CopyToContainer(ctx context.Context, containerName, destDir, fileName string, content []byte, mode int64) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: fileName,
		Mode: mode,
		Size: int64(len(content)),
	}); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("writing tar content: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar writer: %w", err)
	}

	return c.api.CopyToContainer(ctx, containerName, destDir, &buf, container.CopyToContainerOptions{})
}

// CopyFileToContainer reads a file from the host and copies it into a container.
// Useful for injecting custom binaries. The file is made executable (0755).
func (c *Client) CopyFileToContainer(ctx context.Context, containerName, destDir, destName, hostPath string) error {
	content, err := os.ReadFile(hostPath)
	if err != nil {
		return fmt.Errorf("reading host file %s: %w", hostPath, err)
	}
	return c.CopyToContainer(ctx, containerName, destDir, destName, content, 0755)
}

// GetContainerIP returns the IP address of a container on the given network.
// We need IPs to set up iptables rules for network partitions.
func (c *Client) GetContainerIP(ctx context.Context, containerName, networkName string) (string, error) {
	info, err := c.api.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("inspecting %s for IP: %w", containerName, err)
	}

	if info.NetworkSettings == nil {
		return "", fmt.Errorf("container %s has no network settings", containerName)
	}

	ep, ok := info.NetworkSettings.Networks[networkName]
	if !ok {
		return "", fmt.Errorf("container %s is not connected to network %s", containerName, networkName)
	}
	if ep.IPAddress == "" {
		return "", fmt.Errorf("container %s has no IP on network %s", containerName, networkName)
	}

	return ep.IPAddress, nil
}

// Exec runs a command inside a running container and returns the combined output.
// This is used by chaos actions that need to run tools like `tc` inside containers.
func (c *Client) Exec(ctx context.Context, containerName string, cmd []string) (string, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execID, err := c.api.ContainerExecCreate(ctx, containerName, execCfg)
	if err != nil {
		return "", fmt.Errorf("creating exec in %s: %w", containerName, err)
	}

	resp, err := c.api.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return "", fmt.Errorf("attaching to exec in %s: %w", containerName, err)
	}
	defer resp.Close()

	// Docker multiplexes stdout/stderr into a single stream with headers.
	// stdcopy.StdCopy demuxes it.
	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader); err != nil {
		return "", fmt.Errorf("reading exec output from %s: %w", containerName, err)
	}

	// Check exit code.
	inspect, err := c.api.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return outBuf.String(), fmt.Errorf("inspecting exec in %s: %w", containerName, err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("exec in %s exited with code %d: %s", containerName, inspect.ExitCode, errBuf.String())
	}

	return outBuf.String(), nil
}
