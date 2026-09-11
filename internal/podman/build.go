package podman

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

type BuildOptions struct {
	ContextDir string
	Dockerfile string // path relative to ContextDir, default "Dockerfile"
	Tag        string // e.g. "space-elevator/myapp/web:latest"
	BuildArgs  map[string]*string
	Platform   string
	// Log receives the build's progress lines (steps, RUN output).
	// Nil means the stream is only scanned for errors.
	Log func(string)
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
		Tags:        []string{opts.Tag},
		Dockerfile:  opts.Dockerfile,
		BuildArgs:   opts.BuildArgs,
		Remove:      true,
		ForceRemove: true,
	})
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()
	return readBuildOutput(resp.Body, opts.Log)
}

// readBuildOutput drains the build stream, forwarding progress lines
// to log (when non-nil) and surfacing failures. The build API reports
// errors as {"error": ...} messages inside a 200 response, so a failed
// RUN step must be detected here — skipping the body would silently
// leave the previous image under the tag.
func readBuildOutput(r io.Reader, log func(string)) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("image build: reading output: %w", err)
		}
		if log != nil && msg.Stream != "" {
			for _, line := range strings.Split(strings.TrimRight(msg.Stream, "\n"), "\n") {
				if strings.TrimSpace(line) != "" {
					log(line)
				}
			}
		}
		if msg.Error != "" {
			if msg.ErrorDetail.Message != "" {
				return fmt.Errorf("image build failed: %s", msg.ErrorDetail.Message)
			}
			return fmt.Errorf("image build failed: %s", msg.Error)
		}
	}
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
			// VCS metadata and device/socket nodes have no business in a
			// build context (and opening the latter can block forever).
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return nil
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
		Image:        opts.Image,
		Env:          opts.Env,
		Cmd:          opts.Cmd,
		ExposedPorts: exposed,
		Labels:       opts.Labels,
	}
	hostCfg := &container.HostConfig{
		PortBindings:   portBindings,
		Binds:          opts.Binds,
		AutoRemove:     opts.AutoRemove,
		RestartPolicy:  container.RestartPolicy{Name: "unless-stopped"},
		SecurityOpt:    opts.SecurityOpt,
		ReadonlyRootfs: opts.ReadonlyRootfs,
		Tmpfs:          opts.Tmpfs,
		CapDrop:        opts.CapDrop,
		CapAdd:         opts.CapAdd,
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
