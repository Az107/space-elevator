package podman

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/image"
)

type Image struct {
	ID       string
	RepoTags []string
	Size     int64
	Created  time.Time
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	imgs, err := c.cli.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	out := make([]Image, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, Image{
			ID:       im.ID[7:19],
			RepoTags: im.RepoTags,
			Size:     im.Size,
			Created:  time.Unix(im.Created, 0),
		})
	}
	return out, nil
}

func (c *Client) PullImage(ctx context.Context, ref string) error {
	rc, err := c.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// RemoveImage deletes an image by repo tag or ID. Force=true also removes
// any containers that reference it.
func (c *Client) RemoveImage(ctx context.Context, ref string, force bool) error {
	opts := image.RemoveOptions{Force: force}
	items, err := c.cli.ImageRemove(ctx, ref, opts)
	if err != nil {
		return fmt.Errorf("image remove %s: %w", ref, err)
	}
	_ = items
	return nil
}