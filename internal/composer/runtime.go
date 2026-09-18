package composer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/albertoruiz/space-elevator/internal/podman"
)

const (
	LabelApp         = "space-elevator.app"
	LabelService     = "space-elevator.service"
	LabelManaged     = "space-elevator.managed"
	LabelKind        = "space-elevator.kind"
	LabelScaleToZero = "space-elevator.scale-to-zero"
)

type Runtime struct {
	cli              *podman.Client
	appsRoot         string
	DefaultMemory    int64 // bytes; 0 = no limit
	DefaultPidsLimit int64 // 0 = no limit
	// Log receives human-readable progress lines (image builds, pulls,
	// container starts). Nil means discard. Set per deploy by callers
	// that want a live feed (web build panel); CLI prints its own.
	Log func(string)
}

func NewRuntime(cli *podman.Client, appsRoot string) *Runtime {
	return &Runtime{cli: cli, appsRoot: appsRoot}
}

func (r *Runtime) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

// WithLimits overrides the runtime's default per-container resource limits.
// Pass 0 to clear a limit (kernel default).
func (r *Runtime) WithLimits(memoryBytes, pidsLimit int64) *Runtime {
	r.DefaultMemory = memoryBytes
	r.DefaultPidsLimit = pidsLimit
	return r
}

func (r *Runtime) networkName(appName string) string {
	return "se-" + sanitize(appName)
}

func (r *Runtime) imageTag(appName, service string) string {
	return "localhost/se/" + sanitize(appName) + "/" + sanitize(service) + ":latest"
}

// ImageTag returns the image tag that Deploy() builds for a given
// (app, service) pair. Exported so callers outside the runtime can
// clean up the image when the app is removed.
func (r *Runtime) ImageTag(appName, service string) string {
	return r.imageTag(appName, service)
}

func (r *Runtime) containerName(appName, service string) string {
	return "se-" + sanitize(appName) + "-" + sanitize(service)
}

// Deploy brings up an app: ensure network, build/pull images, create+start containers.
func (r *Runtime) Deploy(ctx context.Context, meta AppMeta, spec *Spec, sourceDir string) error {
	netName := r.networkName(meta.Name)
	if err := r.ensureNetwork(ctx, netName); err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}

	if err := r.ensureTopLevelVolumes(ctx, spec, meta); err != nil {
		return fmt.Errorf("ensure volumes: %w", err)
	}

	order := TopologicalOrder(spec)
	for _, svcName := range order {
		svc := spec.Services[svcName]
		if err := r.deployService(ctx, meta, svcName, svc, spec, sourceDir, netName); err != nil {
			return fmt.Errorf("service %q: %w", svcName, err)
		}
	}
	return nil
}

func (r *Runtime) ensureNetwork(ctx context.Context, name string) error {
	nets, err := r.cli.ListNetworks(ctx)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = r.cli.CreateNetwork(ctx, name)
	return err
}

func (r *Runtime) ensureTopLevelVolumes(ctx context.Context, spec *Spec, meta AppMeta) error {
	// Named volumes are lazily created by podman when used in a mount.
	// We only validate that names are sane and namespaced.
	for name := range spec.Volumes {
		if name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid volume name %q", name)
		}
	}
	return nil
}

func (r *Runtime) deployService(ctx context.Context, meta AppMeta, svcName string, svc Service, spec *Spec, sourceDir, netName string) error {
	imageRef := svc.Image
	if svc.Build != nil {
		imageRef = r.imageTag(meta.Name, svcName)
		ctxDir := svc.Build.Context
		if ctxDir == "" {
			ctxDir = "."
		}
		absCtx := filepath.Join(sourceDir, ctxDir)
		if err := r.writeBuildContextEnv(absCtx, meta.BuildEnv); err != nil {
			return fmt.Errorf("build env: %w", err)
		}
		// Plain app env also flows as --build-arg (works for
		// Dockerfiles that declare matching ARG lines).
		buildArgs := make(map[string]*string, len(meta.BuildEnv))
		for k, v := range meta.BuildEnv {
			v := v
			buildArgs[k] = &v
		}
		r.logf("building image %s (context %s)...", imageRef, ctxDir)
		if err := r.cli.BuildImage(ctx, podman.BuildOptions{
			ContextDir: absCtx,
			Dockerfile: svc.Build.Dockerfile,
			Tag:        imageRef,
			BuildArgs:  buildArgs,
			Log:        r.Log,
		}); err != nil {
			return fmt.Errorf("build: %w", err)
		}
		r.logf("built image %s", imageRef)
	} else {
		r.logf("pulling image %s...", imageRef)
		if err := r.cli.PullImage(ctx, imageRef); err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		r.logf("pulled image %s", imageRef)
	}

	env := ResolveEnv(svc.Environment, meta.Env)
	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}

	binds, err := r.resolveBinds(svc.Volumes, sourceDir, spec)
	if err != nil {
		return fmt.Errorf("volumes: %w", err)
	}

	labels := mergeLabels(svc.Labels, map[string]string{
		LabelApp:     meta.Label,
		LabelService: svcName,
		LabelManaged: "1",
	})
	if meta.Kind != "" {
		labels[LabelKind] = meta.Kind
	}
	if meta.ScaleToZero {
		labels[LabelScaleToZero] = "1"
	}

	mem := meta.MemoryBytes
	if mem == 0 {
		mem = r.DefaultMemory
	}
	pids := meta.PidsLimit
	if pids == 0 {
		pids = r.DefaultPidsLimit
	}
	var pidsPtr *int64
	if pids > 0 {
		pidsPtr = &pids
	}

	opts := podman.CreateOptions{
		Name:        r.containerName(meta.Name, svcName),
		Image:       imageRef,
		Env:         envList,
		Cmd:         svc.Command,
		Ports:       svc.Ports,
		Binds:       binds,
		Network:     netName,
		Aliases:     []string{svcName},
		Labels:      labels,
		SecurityOpt: []string{"no-new-privileges:true"},
		PidsLimit:   pidsPtr,
		Memory:      mem,
		// Drop the dangerous capabilities but keep what nginx-style
		// entrypoints need: CHOWN for the tmp dir initialization,
		// DAC_OVERRIDE for log writes as the unprivileged nginx user,
		// FOWNER/FSETID/SETFCAP for chown'd temp dirs, SETUID/SETGID
		// for the master/worker drop, KILL for reaping, NET_BIND_SERVICE
		// to bind low ports, SYS_CHROOT for the chroot() inside the
		// entrypoint.
		CapDrop: []string{
			"CAP_NET_RAW",
			"CAP_SYS_PTRACE",
			"CAP_SYS_ADMIN",
			"CAP_NET_ADMIN",
			"CAP_SYS_MODULE",
			"CAP_SYS_RAWIO",
			"CAP_SYS_BOOT",
			"CAP_AUDIT_WRITE",
			"CAP_AUDIT_CONTROL",
			"CAP_SYSLOG",
			"CAP_SYS_TIME",
			"CAP_SYS_TTY_CONFIG",
			"CAP_MKNOD",
			"CAP_LEASE",
			"CAP_AUDIT_READ",
			"CAP_BLOCK_SUSPEND",
			"CAP_IPC_LOCK",
			"CAP_IPC_OWNER",
			"CAP_MAC_OVERRIDE",
			"CAP_SYS_RESOURCE",
			"CAP_SYS_NICE",
			"CAP_SYS_PACCT",
			"CAP_WAKE_ALARM",
		},
	}
	if meta.StaticDrop {
		// Read-only root FS for synth nginx:alpine drops. nginx still
		// needs to write to /var/cache/nginx, /var/run, /tmp — those
		// are tmpfs.
		opts.ReadonlyRootfs = true
		opts.Tmpfs = map[string]string{
			"/var/cache/nginx": "rw,size=16m",
			"/var/run":         "rw,size=1m",
			"/tmp":             "rw,size=16m",
		}
	}
	_, err = r.cli.CreateContainer(ctx, opts)
	if err != nil {
		return err
	}
	r.logf("starting container %s...", opts.Name)
	return r.cli.StartContainer(ctx, r.containerName(meta.Name, svcName))
}

// buildEnvMarker marks the managed .env.local so a later deploy can
// distinguish its own file from one committed by the repo.
const buildEnvMarker = "# generated by space-elevator (app env, build-time reads)"

// writeBuildContextEnv exposes the app's plain env to the image build
// by writing .env.local into the build context — the standard location
// that Vite, Next.js, CRA and friends load automatically during
// `build`, so front-end bundlers inline VITE_/NEXT_PUBLIC_ vars without
// requiring Dockerfile changes. A repo-provided .env.local is left
// alone. Only plain env reaches the build context; secrets never do.
func (r *Runtime) writeBuildContextEnv(absCtx string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	path := filepath.Join(absCtx, ".env.local")
	if existing, err := os.ReadFile(path); err == nil {
		lines := strings.SplitN(string(existing), "\n", 2)
		if lines[0] != buildEnvMarker {
			r.logf("note: build context ships its own .env.local; leaving it untouched (app env still passed as build args)")
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	var b strings.Builder
	b.WriteString(buildEnvMarker + "\n")
	for _, k := range keys {
		b.WriteString(k + "=" + env[k] + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	r.logf("injected %d env var(s) into the build context (.env.local)", len(keys))
	return nil
}

// resolveBinds translates compose volume mounts into podman binds.
//
// Host-path binds are the classic container-escape hatch: an absolute
// path in a compose file would mount any host directory straight into
// the container. Only named volumes and paths relative to the app's own
// source dir (contained there) are allowed.
func (r *Runtime) resolveBinds(mounts []string, sourceDir string, spec *Spec) ([]string, error) {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts := strings.SplitN(m, ":", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid volume %q", m)
		}
		src := parts[0]
		// Named volume: defined in spec.Volumes or syntactically (not a path)
		if _, ok := spec.Volumes[src]; ok {
			out = append(out, m)
			continue
		}
		if strings.HasPrefix(src, "/") {
			return nil, fmt.Errorf("volume %q: absolute host paths are not allowed", m)
		}
		if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
			// Resolve relative paths against the app's source dir and
			// refuse any result that escapes it.
			abs := filepath.Join(sourceDir, src)
			rel, err := filepath.Rel(sourceDir, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				return nil, fmt.Errorf("volume %q: path escapes the app source dir", m)
			}
			out = append(out, abs+":"+strings.Join(parts[1:], ":"))
			continue
		}
		// Treat as named volume
		out = append(out, m)
	}
	return out, nil
}

// Start starts every container of an app. Errors when the app has no
// containers to start (nothing deployed yet, or already removed).
func (r *Runtime) Start(ctx context.Context, meta AppMeta) error {
	return r.eachContainer(ctx, meta, func(full string) error {
		return r.cli.StartContainer(ctx, full)
	})
}

// Stop stops every container of an app, preserving them for a later Start.
func (r *Runtime) Stop(ctx context.Context, meta AppMeta) error {
	return r.eachContainer(ctx, meta, func(full string) error {
		return r.cli.StopContainer(ctx, full, 10)
	})
}

// Restart stops then starts every container (env is baked at create
// time, so this does not pick up env/secret edits).
func (r *Runtime) Restart(ctx context.Context, meta AppMeta) error {
	return r.eachContainer(ctx, meta, func(full string) error {
		_ = r.cli.StopContainer(ctx, full, 10)
		return r.cli.StartContainer(ctx, full)
	})
}

// eachContainer runs fn against the full ID of every container labeled
// for the app. Label lookups use meta.Label (the app's stable slug).
func (r *Runtime) eachContainer(ctx context.Context, meta AppMeta, fn func(full string) error) error {
	cs, err := r.cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {LabelApp + "=" + meta.Label},
	})
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return fmt.Errorf("no containers for %q; deploy it first", meta.Name)
	}
	for _, c := range cs {
		full, err := r.cli.LookupID(ctx, c.ID)
		if err != nil || full == "" {
			continue
		}
		if err := fn(full); err != nil {
			return err
		}
	}
	return nil
}

// Remove tears down all containers + the network for an app. Volumes are preserved.
func (r *Runtime) Remove(ctx context.Context, meta AppMeta, spec *Spec) error {
	cs, err := r.cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {LabelApp + "=" + meta.Label},
	})
	if err != nil {
		return err
	}
	for _, ctr := range cs {
		full, err := r.cli.LookupID(ctx, ctr.ID)
		if err != nil {
			continue
		}
		_ = r.cli.StopContainer(ctx, full, 10)
		if err := r.cli.RemoveContainer(ctx, full, true); err != nil {
			return fmt.Errorf("remove %s: %w", full, err)
		}
	}
	if err := r.cli.RemoveNetwork(ctx, r.networkName(meta.Name)); err != nil {
		// ignore: network may already be gone
	}
	return nil
}

func (r *Runtime) removeNetwork(ctx context.Context, name string) error {
	if err := r.cli.RemoveNetwork(ctx, name); err != nil {
		// ignore "not found"
		return nil
	}
	return nil
}

// Status computes the runtime status of an app.
func (r *Runtime) Status(ctx context.Context, meta AppMeta, spec *Spec) (string, []ContainerInfo, error) {
	cs, err := r.cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {LabelApp + "=" + meta.Label},
	})
	if err != nil {
		return "error", nil, err
	}
	if len(cs) == 0 {
		return "stopped", nil, nil
	}
	running := 0
	want := len(spec.Services)
	services := make([]ContainerInfo, 0, len(cs))
	for _, c := range cs {
		// We don't have service label in our abbreviated Container type; refetch.
		info := ContainerInfo{Name: c.Name, State: c.State, Status: c.Status, ID: c.ID}
		services = append(services, info)
		if c.State == "running" {
			running++
		}
	}
	switch {
	case running == want:
		return "running", services, nil
	case running == 0:
		return "stopped", services, nil
	default:
		return "partial", services, nil
	}
}

type ContainerInfo struct {
	ID      string
	Name    string
	Service string
	State   string
	Status  string
}

// Logs streams logs for one or all services of an app.
func (r *Runtime) Logs(ctx context.Context, meta AppMeta, service string, follow bool, tail string) (io.ReadCloser, error) {
	prefix := r.containerName(meta.Name, service)
	if service == "" || service == "*" {
		prefix = ""
	}
	cs, err := r.cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {LabelApp + "=" + meta.Label},
	})
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, errors.New("no containers")
	}
	// Pick best match: by service name, else first
	var pick = cs[0].ID
	if service != "" && service != "*" {
		for _, c := range cs {
			if c.Name == prefix {
				pick = c.ID
				break
			}
		}
	}
	return r.cli.ContainerLogs(ctx, pick, follow, tail)
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sanitize(s string) string {
	return SanitizeForImage(s)
}

// SanitizeForImage rewrites an arbitrary string into a form that's safe to
// use as a single label in a container image tag. Non-alphanumeric chars
// become '-'. Lower-cased so two names that differ only in case still map
// to the same tag.
func SanitizeForImage(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return strings.ToLower(string(out))
}
