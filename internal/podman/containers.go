package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
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
	return c.ListContainersFiltered(ctx, all, nil)
}

func (c *Client) ListContainersFiltered(ctx context.Context, all bool, f map[string][]string) ([]Container, error) {
	args := filters.NewArgs()
	for k, vs := range f {
		for _, v := range vs {
			args.Add(k, v)
		}
	}
	cs, err := c.cli.ContainerList(ctx, container.ListOptions{All: all, Filters: args})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]Container, 0, len(cs))
	for _, ctr := range cs {
		id := ctr.ID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, Container{
			ID:      id,
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

// ErrContainerNotFound reports that no container matched an ID prefix. It is
// distinct from a transport/API failure so callers can skip a vanished
// container without swallowing a real error.
var ErrContainerNotFound = errors.New("container not found")

// LookupID resolves a full or partial container ID.
//
// A failed Podman call must not be reported as "not found": callers such as
// eachContainer, RemoveContainers, and InspectIPs skip on not-found, so
// conflating the two lets Start report success having started nothing, and
// lets RemoveContainers report success while containers survive to collide
// with the next deployment.
func (c *Client) LookupID(ctx context.Context, prefix string) (string, error) {
	cs, err := c.ListContainersFiltered(ctx, true, map[string][]string{"id": {prefix}})
	if err != nil {
		return "", fmt.Errorf("lookup container %q: %w", prefix, err)
	}
	for _, ctr := range cs {
		if len(ctr.ID) >= len(prefix) && ctr.ID[:len(prefix)] == prefix {
			return ctr.ID, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrContainerNotFound, prefix)
}

func formatPorts(ports []container.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type))
	}
	return out
}
