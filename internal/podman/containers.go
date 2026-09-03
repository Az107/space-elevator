package podman

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
)

type Container struct {
	ID      string
	Name    string
	Image   string
	State   string
	Status  string
	Ports   []string
	Created time.Time
}

func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	cs, err := c.cli.ContainerList(ctx, container.ListOptions{All: all})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]Container, 0, len(cs))
	for _, ctr := range cs {
		out = append(out, Container{
			ID:      ctr.ID[:12],
			Name:    strings.Join(ctr.Names, ", "),
			Image:   ctr.Image,
			State:   ctr.State,
			Status:  ctr.Status,
			Ports:   formatPorts(ctr.Ports),
			Created: time.Unix(ctr.Created, 0),
		})
	}
	return out, nil
}

func (c *Client) ContainerLogs(ctx context.Context, id string, follow bool, tail string) (io.ReadCloser, error) {
	return c.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	})
}

func (c *Client) StopContainer(ctx context.Context, id string, timeout int) error {
	return c.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	return c.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

func formatPorts(ports []container.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type))
	}
	return out
}