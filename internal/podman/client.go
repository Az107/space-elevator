package podman

import (
	"context"
	"net/http"

	"github.com/docker/docker/client"
	"github.com/docker/docker/api/types"
)

type Client struct {
	cli *client.Client
}

func New(socketPath string) (*Client, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{},
	}
	opts := []client.Opt{
		client.WithHTTPClient(httpClient),
		client.WithHost("unix://" + socketPath),
		client.WithAPIVersionNegotiation(),
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &Client{cli: cli}, nil
}

func (c *Client) Ping(ctx context.Context) (types.Ping, error) {
	return c.cli.Ping(ctx)
}

func (c *Client) Close() error {
		return c.cli.Close()
}

func (c *Client) Raw() *client.Client {
	return c.cli
}