package podman

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

type BuildOptions struct {
	ContextDir   string
	Dockerfile   string   // path relative to ContextDir, default "Dockerfile"
	Tag          string   // e.g. "space-elevator/myapp/web:latest"
	BuildArgs    map[string]*string
	Platform     string
}

func (c *Client) BuildImage(ctx context.Context, opts BuildOptions) error {
	if opts.Dockerfile == "" {
		opts.Dockerfile = "Dockerfile"
	}
	tarCtx, err := tarContext(opts.ContextDir)
	if err != nil {
		return err
	}
	defer tarCtx.Close()

	resp, err := c.cli.ImageBuild(ctx, tarCtx, build.ImageBuildOptions{
		Tags:       []string{opts.Tag},
		Dockerfile: opts.Dockerfile,
		BuildArgs:  opts.BuildArgs,
		Remove:     true,
		ForceRemove: true,
	})
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func tarContext(dir string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	tw := tar.NewWriter(pw)
	go func() {
		defer pw.Close()
		defer tw.Close()
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			if rel == "." {
				rel = "."
			}
			hdr := &tar.Header{
				Name:     rel,
				Mode:     int64(info.Mode().Perm()),
				ModTime:  info.ModTime(),
				Typeflag: tar.TypeReg,
				Size:     info.Size(),
			}
			if info.IsDir() {
				hdr.Typeflag = tar.TypeDir
				hdr.Size = 0
				// Directories must always be traversable in the image —
				// some images (e.g. nginx:alpine) drop privileges and the
				// worker can't enter dirs that lack the execute bit.
				if hdr.Mode&0o111 == 0 {
					hdr.Mode |= 0o111
				}
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			pw.CloseWithError(err)
		}
	}()
	return pr, nil
}

type CreateOptions struct {
	Name           string
	Image          string
	Env            []string
	Cmd            []string
	Ports          []string // "host:container"
	Binds          []string // "src:dst[:mode]"
	Network        string   // attach to this network
	Aliases        []string
	Labels         map[string]string
	AutoRemove     bool
	SecurityOpt    []string // e.g. ["no-new-privileges:true"]
	PidsLimit      *int64   // nil = no limit
	Memory         int64    // bytes; 0 = no limit
	ReadonlyRootfs bool
	Tmpfs          map[string]string
	// CapAdd / CapDrop let callers strip privileges (e.g. drop ALL then
	// add only NET_BIND_SERVICE for apps that need to bind low ports).
	CapDrop []string
	CapAdd  []string
}

func (c *Client) CreateContainer(ctx context.Context, opts CreateOptions) (string, error) {
	exposed, portBindings, err := parsePorts(opts.Ports)
	if err != nil {
		return "", err
	}

	cfg := &container.Config{
		Image:       opts.Image,
		Env:         opts.Env,
		Cmd:         opts.Cmd,
		ExposedPorts: exposed,
		Labels:      opts.Labels,
	}
	hostCfg := &container.HostConfig{
		PortBindings:  portBindings,
		Binds:         opts.Binds,
		AutoRemove:    opts.AutoRemove,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		SecurityOpt:   opts.SecurityOpt,
		ReadonlyRootfs: opts.ReadonlyRootfs,
		Tmpfs:         opts.Tmpfs,
		CapDrop:       opts.CapDrop,
		CapAdd:        opts.CapAdd,
		Resources: container.Resources{
			Memory:    opts.Memory,
			PidsLimit: opts.PidsLimit,
		},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			opts.Network: {Aliases: opts.Aliases},
		},
	}
	if opts.Network == "" {
		netCfg = nil
	}

	resp, err := c.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

func parsePorts(ports []string) (map[nat.Port]struct{}, map[nat.Port][]nat.PortBinding, error) {
	if len(ports) == 0 {
		return nil, nil, nil
	}
	return nat.ParsePortSpecs(ports)
}
