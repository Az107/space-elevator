package podman

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/volume"
)

type dockerVolumeClient interface {
	VolumeCreate(context.Context, volume.CreateOptions) (volume.Volume, error)
	VolumeList(context.Context, volume.ListOptions) (volume.ListResponse, error)
	VolumeInspect(context.Context, string) (volume.Volume, error)
	VolumeRemove(context.Context, string, bool) error
	CreateVolume(context.Context, volume.CreateOptions) (volume.Volume, error)
	ListVolumes(context.Context, volume.ListOptions) (volume.ListResponse, error)
	InspectVolume(context.Context, string) (volume.Volume, error)
	RemoveVolume(context.Context, string, bool) error
}

var _ dockerVolumeClient = (*Client)(nil)

func TestVolumeTypesPreserveLabelsAndUsageData(t *testing.T) {
	got := volume.Volume{
		Labels: map[string]string{"com.example.owner": "space-elevator"},
		UsageData: &volume.UsageData{
			RefCount: 2,
			Size:     4096,
		},
	}

	if got.Labels["com.example.owner"] != "space-elevator" {
		t.Fatalf("labels = %v", got.Labels)
	}
	if got.UsageData == nil || got.UsageData.RefCount != 2 || got.UsageData.Size != 4096 {
		t.Fatalf("usage data = %+v", got.UsageData)
	}
}
