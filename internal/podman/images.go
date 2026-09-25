package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
		id := im.ID
		if len(id) > 7 {
			id = strings.TrimPrefix(id, "sha256:")
		}
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, Image{
			ID:       id,
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
	decoder := json.NewDecoder(rc)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read image pull response: %w", err)
		}
		if msg.Error != "" {
			if msg.ErrorDetail.Message != "" {
				return fmt.Errorf("image pull failed: %s", msg.ErrorDetail.Message)
			}
			return fmt.Errorf("image pull failed: %s", msg.Error)
		}
	}
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
