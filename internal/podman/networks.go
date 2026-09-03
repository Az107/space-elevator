package podman

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/network"
)

type Network struct {
	ID     string
	Name   string
	Driver string
	Scope  string
	Subnet string
}

func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	nets, err := c.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	out := make([]Network, 0, len(nets))
	for _, n := range nets {
		subnet := ""
		if len(n.IPAM.Config) > 0 {
			subnet = n.IPAM.Config[0].Subnet
		}
		out = append(out, Network{
			ID:     n.ID[:12],
			Name:   n.Name,
			Driver: n.Driver,
			Scope:  n.Scope,
			Subnet: subnet,
		})
	}
	return out, nil
}

func (c *Client) CreateNetwork(ctx context.Context, name string) (string, error) {
	resp, err := c.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}