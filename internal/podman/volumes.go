package podman

import (
	"context"

	"github.com/docker/docker/api/types/volume"
)

// These aliases keep the Docker v28 API types available to callers without
// translating or dropping fields such as labels and usage data.
type (
	Volume              = volume.Volume
	VolumeCreateOptions = volume.CreateOptions
	VolumeListOptions   = volume.ListOptions
	VolumeListResponse  = volume.ListResponse
	VolumeUsageData     = volume.UsageData
)

// VolumeCreate is the Docker-compatible name for creating a volume.
func (c *Client) VolumeCreate(ctx context.Context, options volume.CreateOptions) (volume.Volume, error) {
	return c.cli.VolumeCreate(ctx, options)
}

// CreateVolume creates a volume using Docker-compatible volume options.
func (c *Client) CreateVolume(ctx context.Context, options volume.CreateOptions) (volume.Volume, error) {
	return c.VolumeCreate(ctx, options)
}

// VolumeList is the Docker-compatible name for listing volumes.
func (c *Client) VolumeList(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error) {
	return c.cli.VolumeList(ctx, options)
}

// ListVolumes lists volumes using Docker-compatible volume options. Labels and
// usage data are preserved when Podman includes them in the API response.
func (c *Client) ListVolumes(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error) {
	return c.VolumeList(ctx, options)
}

// VolumeInspect is the Docker-compatible name for inspecting a volume.
func (c *Client) VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error) {
	return c.cli.VolumeInspect(ctx, volumeID)
}

// InspectVolume inspects a volume by name or ID.
func (c *Client) InspectVolume(ctx context.Context, volumeID string) (volume.Volume, error) {
	return c.VolumeInspect(ctx, volumeID)
}

// VolumeRemove is the Docker-compatible name for removing a volume.
func (c *Client) VolumeRemove(ctx context.Context, volumeID string, force bool) error {
	return c.cli.VolumeRemove(ctx, volumeID, force)
}

// RemoveVolume removes a volume by name or ID.
func (c *Client) RemoveVolume(ctx context.Context, volumeID string, force bool) error {
	return c.VolumeRemove(ctx, volumeID, force)
}
