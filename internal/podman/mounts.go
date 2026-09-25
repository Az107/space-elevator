package podman

import (
	"context"
	"fmt"
)

// ContainerMount is the subset of a Docker/Podman mount needed to discover
// which physical volume backs a compose logical volume during first update.
type ContainerMount struct {
	Type        string
	Name        string
	Source      string
	Destination string
	ReadWrite   bool
}

func (c *Client) ContainerMounts(ctx context.Context, id string) ([]ContainerMount, error) {
	full, err := c.LookupID(ctx, id)
	if err != nil {
		return nil, err
	}
	inspect, err := c.cli.ContainerInspect(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("inspect mounts %s: %w", id, err)
	}
	out := make([]ContainerMount, 0, len(inspect.Mounts))
	for _, m := range inspect.Mounts {
		out = append(out, ContainerMount{
			Type: string(m.Type), Name: m.Name, Source: m.Source,
			Destination: m.Destination, ReadWrite: m.RW,
		})
	}
	return out, nil
}
