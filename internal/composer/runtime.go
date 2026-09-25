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
	"github.com/docker/docker/api/types/volume"
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

// ReleaseImageTag returns an immutable tag for a candidate release. Release
// IDs are generated UUIDs, so the image built for an update cannot overwrite
// the image currently used by the previous release.
func (r *Runtime) ReleaseImageTag(appName, service, releaseID string) string {
	return "localhost/se/" + sanitize(appName) + "/" + sanitize(service) + ":" + sanitize(releaseID)
}

func (r *Runtime) containerName(appName, service string) string {
	return "se-" + sanitize(appName) + "-" + sanitize(service)
}

// Deploy brings up an app: ensure network and storage, build/pull images,
// then create and start containers. Update flows use Prepare and Activate
// separately so a candidate can be built before the old release is stopped.
func (r *Runtime) Deploy(ctx context.Context, meta AppMeta, spec *Spec, sourceDir string) error {
	images, err := r.Prepare(ctx, meta, spec, sourceDir)
	if err != nil {
		return err
	}
	return r.Activate(ctx, meta, spec, sourceDir, images)
}

// Prepare validates runtime resources and builds or pulls every service image.
// It does not create containers, so failure leaves an existing deployment
// untouched.
func (r *Runtime) Prepare(ctx context.Context, meta AppMeta, spec *Spec, sourceDir string) (map[string]string, error) {
	if spec == nil {
		return nil, fmt.Errorf("compose spec is nil")
	}
	if err := r.ensureNetwork(ctx, r.networkName(meta.Name)); err != nil {
		return nil, fmt.Errorf("ensure network: %w", err)
	}
	if err := r.ensureTopLevelVolumes(ctx, spec, meta); err != nil {
		return nil, fmt.Errorf("ensure volumes: %w", err)
	}
	images := make(map[string]string, len(spec.Services))
	for _, svcName := range TopologicalOrder(spec) {
		image, err := r.prepareServiceImage(ctx, meta, svcName, spec.Services[svcName], spec, sourceDir)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		images[svcName] = image
	}
	return images, nil
}

// Activate creates and starts containers from images already returned by
// Prepare. No image build or pull happens here, keeping the downtime window
// limited to storage cutover and container startup.
func (r *Runtime) Activate(ctx context.Context, meta AppMeta, spec *Spec, sourceDir string, images map[string]string) error {
	if spec == nil {
		return fmt.Errorf("compose spec is nil")
	}
	if err := r.ensureNetwork(ctx, r.networkName(meta.Name)); err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}
	if err := r.ensureTopLevelVolumes(ctx, spec, meta); err != nil {
		return fmt.Errorf("ensure volumes: %w", err)
	}
	for _, svcName := range TopologicalOrder(spec) {
		image := images[svcName]
		if image == "" {
			image = meta.ImageTags[svcName]
		}
		if image == "" {
			image = spec.Services[svcName].Image
		}
		if image == "" {
			return fmt.Errorf("service %q: prepared image is missing", svcName)
		}
		if err := r.activateService(ctx, meta, svcName, spec.Services[svcName], spec, sourceDir, r.networkName(meta.Name), image); err != nil {
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

func (r *Runtime) ensureManagedBind(meta AppMeta, ref string) error {
	if !filepath.IsAbs(ref) {
		return fmt.Errorf("managed bind path is not absolute")
	}
	if r.appsRoot == "" {
		return os.MkdirAll(ref, 0o700)
	}
	root := filepath.Join(r.appsRoot, "data", meta.ID)
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed data root is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if _, err := containedPath(root, ref); err != nil {
		return err
	}
	return os.MkdirAll(ref, 0o700)
}

func (r *Runtime) ensureTopLevelVolumes(ctx context.Context, spec *Spec, meta AppMeta) error {
	if spec == nil {
		return fmt.Errorf("compose spec is nil")
	}
	for name, binding := range meta.Storage {
		if name == "" {
			return fmt.Errorf("invalid empty logical volume name")
		}
		if binding.Kind == StorageKindBind {
			if err := r.ensureManagedBind(meta, binding.Ref); err != nil {
				return fmt.Errorf("managed bind %q: %w", name, err)
			}
		}
	}
	for name := range spec.Volumes {
		if name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid volume name %q", name)
		}
		if meta.ID == "" {
			return fmt.Errorf("app id is required to scope volume %q", name)
		}
		physical := ManagedVolumeName(meta.ID, name)
		if binding, ok := meta.Storage[name]; ok && binding.Kind == StorageKindVolume && binding.Ref != "" {
			physical = binding.Ref
		}
		if err := r.createVolume(ctx, physical, meta.Label); err != nil {
			return err
		}
	}
	for name, binding := range meta.Storage {
		if binding.Kind != StorageKindVolume || binding.Ref == "" {
			continue
		}
		if _, declared := spec.Volumes[name]; declared {
			continue
		}
		if err := r.createVolume(ctx, binding.Ref, meta.Label); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) createVolume(ctx context.Context, physical, appLabel string) error {
	if existing, err := r.cli.InspectVolume(ctx, physical); err == nil && appLabel != "" {
		if owner := existing.Labels[LabelApp]; owner != "" && owner != appLabel {
			return fmt.Errorf("volume %q is already owned by app %q", physical, owner)
		}
	}
	labels := map[string]string{}
	if appLabel != "" {
		labels[LabelApp] = appLabel
		labels[LabelManaged] = "1"
	}
	if _, err := r.cli.CreateVolume(ctx, volume.CreateOptions{Name: physical, Labels: labels}); err != nil {
		return fmt.Errorf("create volume %q: %w", physical, err)
	}
	return nil
}

func (r *Runtime) prepareServiceImage(ctx context.Context, meta AppMeta, svcName string, svc Service, spec *Spec, sourceDir string) (string, error) {
	imageRef := svc.Image
	if svc.Build != nil {
		if override := meta.ImageTags[svcName]; override != "" {
			imageRef = override
		} else {
			imageRef = r.imageTag(meta.Name, svcName)
		}
		ctxDir := svc.Build.Context
		if ctxDir == "" {
			ctxDir = "."
		}
		absCtx, err := resolveBuildContext(sourceDir, ctxDir)
		if err != nil {
			return "", fmt.Errorf("build context: %w", err)
		}
		if err := r.writeBuildContextEnv(absCtx, meta.BuildEnv); err != nil {
			return "", fmt.Errorf("build env: %w", err)
		}
		buildArgs := make(map[string]*string, len(meta.BuildEnv))
		for k, v := range meta.BuildEnv {
			v := v
			buildArgs[k] = &v
		}
		r.logf("building image %s (context %s)...", imageRef, ctxDir)
		if err := r.cli.BuildImage(ctx, podman.BuildOptions{
			ContextDir: absCtx, Dockerfile: svc.Build.Dockerfile, Tag: imageRef,
			BuildArgs: buildArgs, Log: r.Log,
		}); err != nil {
			return "", fmt.Errorf("build: %w", err)
		}
		r.logf("built image %s", imageRef)
		return imageRef, nil
	}
	if imageRef == "" {
		return "", fmt.Errorf("service has neither image nor build")
	}
	r.logf("pulling image %s...", imageRef)
	if err := r.cli.PullImage(ctx, imageRef); err != nil {
		return "", fmt.Errorf("pull: %w", err)
	}
	r.logf("pulled image %s", imageRef)
	return imageRef, nil
}

func (r *Runtime) activateService(ctx context.Context, meta AppMeta, svcName string, svc Service, spec *Spec, sourceDir, netName, imageRef string) error {
	env := ResolveEnv(svc.Environment, meta.Env)
	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	binds, err := r.resolveBindsWithStorageForApp(svc.Volumes, sourceDir, spec, meta.Storage, meta.ID)
	if err != nil {
		return fmt.Errorf("volumes: %w", err)
	}
	labels := mergeLabels(svc.Labels, map[string]string{
		LabelApp: meta.Label, LabelService: svcName, LabelManaged: "1",
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
		Name: r.containerName(meta.Name, svcName), Image: imageRef, Env: envList,
		Cmd: svc.Command, Ports: svc.Ports, Binds: binds, Network: netName,
		Aliases: []string{svcName}, Labels: labels,
		SecurityOpt: []string{"no-new-privileges:true"}, PidsLimit: pidsPtr, Memory: mem,
		CapDrop: []string{
			"CAP_NET_RAW", "CAP_SYS_PTRACE", "CAP_SYS_ADMIN", "CAP_NET_ADMIN",
			"CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_BOOT", "CAP_AUDIT_WRITE",
			"CAP_AUDIT_CONTROL", "CAP_SYSLOG", "CAP_SYS_TIME",
			"CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_READ", "CAP_BLOCK_SUSPEND",
			"CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_MAC_OVERRIDE", "CAP_SYS_RESOURCE",
			"CAP_SYS_NICE", "CAP_SYS_PACCT", "CAP_SYS_BOOT", "CAP_WAKE_ALARM",
		},
	}
	if meta.StaticDrop {
		opts.ReadonlyRootfs = true
		opts.Tmpfs = map[string]string{
			"/var/cache/nginx": "rw,size=16m", "/var/run": "rw,size=1m", "/tmp": "rw,size=16m",
		}
	}
	if _, err := r.cli.CreateContainer(ctx, opts); err != nil {
		return err
	}
	r.logf("starting container %s...", opts.Name)
	return r.cli.StartContainer(ctx, opts.Name)
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
	realCtx, err := filepath.EvalSymlinks(absCtx)
	if err != nil {
		return fmt.Errorf("resolve build context: %w", err)
	}
	path := filepath.Join(realCtx, ".env.local")
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlink %q", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	managed := readErr == nil && strings.SplitN(string(existing), "\n", 2)[0] == buildEnvMarker

	if len(env) == 0 {
		if managed {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}

	keys := make([]string, 0, len(env))
	for k, value := range env {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("build env %q contains a line break or NUL", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if readErr == nil && !managed {
		r.logf("note: build context ships its own .env.local; leaving it untouched (app env still passed as build args)")
		return nil
	}
	var b strings.Builder
	b.WriteString(buildEnvMarker + "\n")
	for _, k := range keys {
		b.WriteString(k + "=" + env[k] + "\n")
	}
	tmp, err := os.CreateTemp(realCtx, ".space-elevator-env-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
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
	return r.resolveBindsWithStorage(mounts, sourceDir, spec, nil)
}

func (r *Runtime) resolveBindsWithStorage(mounts []string, sourceDir string, spec *Spec, storage map[string]StorageBinding) ([]string, error) {
	return r.resolveBindsWithStorageForApp(mounts, sourceDir, spec, storage, "")
}

func (r *Runtime) resolveBindsWithStorageForApp(mounts []string, sourceDir string, spec *Spec, storage map[string]StorageBinding, appID string) ([]string, error) {
	if spec == nil {
		return nil, fmt.Errorf("compose spec is nil")
	}
	out := make([]string, 0, len(mounts))
	for _, raw := range mounts {
		mount, err := ParseVolumeMount(raw)
		if err != nil {
			return nil, err
		}
		physical := mount.Source
		if strings.HasPrefix(mount.Source, "/") {
			return nil, fmt.Errorf("volume %q: absolute host paths are not allowed", raw)
		}
		key := CanonicalStorageKey(mount.Source)
		if _, declared := spec.Volumes[mount.Source]; !declared &&
			(strings.HasPrefix(mount.Source, "./") || strings.HasPrefix(mount.Source, "../")) {
			abs, err := containedPath(sourceDir, filepath.Join(sourceDir, mount.Source))
			if err != nil {
				return nil, fmt.Errorf("volume %q: %w", raw, err)
			}
			physical = abs
			if binding, ok := storage[key]; ok && binding.Kind == StorageKindBind && !mount.ReadOnly {
				if !filepath.IsAbs(binding.Ref) {
					return nil, fmt.Errorf("volume %q: managed bind path is not absolute", raw)
				}
				physical = binding.Ref
			}
		} else {
			if appID != "" {
				physical = ManagedVolumeName(appID, key)
			}
			if binding, ok := storage[key]; ok && binding.Ref != "" {
				physical = binding.Ref
			}
		}
		value := physical + ":" + mount.Target
		if mount.Mode != "" && mount.Mode != "rw" {
			value += ":" + mount.Mode
		}
		out = append(out, value)
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
// It is idempotent: a container that is already stopped is not an error.
// That matters for the update transaction, which calls Stop on an app whose
// Status may be "partial" — without this, a half-stopped app could never be
// updated because the already-down container aborts the whole Stop.
func (r *Runtime) Stop(ctx context.Context, meta AppMeta) error {
	return r.eachContainer(ctx, meta, func(full string) error {
		if err := r.cli.StopContainer(ctx, full, 10); err != nil && !alreadyStopped(err) {
			return err
		}
		return nil
	})
}

// Restart stops then starts every container (env is baked at create
// time, so this does not pick up env/secret edits).
func (r *Runtime) Restart(ctx context.Context, meta AppMeta) error {
	return r.eachContainer(ctx, meta, func(full string) error {
		if err := r.cli.StopContainer(ctx, full, 10); err != nil && !alreadyStopped(err) {
			return fmt.Errorf("stop %s: %w", full, err)
		}
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
	handled := 0
	for _, c := range cs {
		full, err := r.cli.LookupID(ctx, c.ID)
		if err != nil {
			// Only a container that vanished between the list and the lookup
			// is skippable. A transport error must abort, otherwise Start
			// reports success having started nothing.
			if errors.Is(err, podman.ErrContainerNotFound) {
				continue
			}
			return err
		}
		if full == "" {
			continue
		}
		if err := fn(full); err != nil {
			return err
		}
		handled++
	}
	if handled == 0 && len(cs) > 0 {
		return fmt.Errorf("none of the %d containers for %q could be resolved", len(cs), meta.Name)
	}
	return nil
}

// Remove tears down all containers + the network for an app. Volumes are preserved.
func (r *Runtime) Remove(ctx context.Context, meta AppMeta, spec *Spec) error {
	if err := r.RemoveContainers(ctx, meta); err != nil {
		return err
	}
	if err := r.cli.RemoveNetwork(ctx, r.networkName(meta.Name)); err != nil {
		// ignore: network may already be gone
	}
	return nil
}

// RemoveContainers removes only app containers. Update cutover and rollback
// use it to preserve the network and all persistent storage.
func (r *Runtime) RemoveContainers(ctx context.Context, meta AppMeta) error {
	cs, err := r.cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {LabelApp + "=" + meta.Label},
	})
	if err != nil {
		return err
	}
	removed := 0
	for _, ctr := range cs {
		full, err := r.cli.LookupID(ctx, ctr.ID)
		if err != nil {
			if errors.Is(err, podman.ErrContainerNotFound) {
				continue
			}
			// Propagating matters: silently skipping here would report a
			// successful teardown while containers survive, and the next
			// deploy would then collide on container names.
			return err
		}
		if err := r.cli.StopContainer(ctx, full, 10); err != nil && !alreadyStopped(err) {
			return fmt.Errorf("stop %s: %w", full, err)
		}
		if err := r.cli.RemoveContainer(ctx, full, true); err != nil {
			return fmt.Errorf("remove %s: %w", full, err)
		}
		removed++
	}
	if removed == 0 && len(cs) > 0 {
		return fmt.Errorf("none of the %d containers for %q could be removed", len(cs), meta.Name)
	}
	return nil
}

// Status computes the runtime status of an app.
func (r *Runtime) Status(ctx context.Context, meta AppMeta, spec *Spec) (string, []ContainerInfo, error) {
	if spec == nil {
		return "error", nil, fmt.Errorf("compose spec is nil")
	}
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

func alreadyStopped(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not running") || strings.Contains(message, "already stopped")
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
