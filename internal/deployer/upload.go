package deployer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// UploadRequest describes a deploy from an uploaded archive. The archive
// is extracted into the app's drop directory; the kind decides how the
// source is turned into a container: web auto-detects static/dockerfile,
// custom requires a Dockerfile, function generates an adapter.
type UploadRequest struct {
	// Name is the app name; a "drop-<id>" name is generated when empty.
	Name string
	// Kind is "web" (default), "function", or "custom".
	Kind           string
	Runtime        string
	RuntimeVersion string
	Entrypoint     string
	// SourceRef is the original filename, shown on the app page.
	SourceRef   string
	Env         map[string]string
	Secrets     map[string]string
	ArchivePath string
	ScaleToZero bool
	IdleTimeout int
}

// CreateUpload extracts an archive, synthesizes the build files for the
// requested kind, and creates the pending app row. It does not build or
// run anything: the caller follows with Redeploy (synchronously for the
// CLI, in a goroutine for web/API so the HTTP request can return).
func (d *Deployer) CreateUpload(ctx context.Context, req UploadRequest) (*store.App, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive required")
	}
	id := uuid.NewString()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "drop-" + id[:8]
	}
	if !ValidAppName(name) {
		return nil, fmt.Errorf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", name)
	}
	if existing, err := d.Store.GetAppByName(ctx, name); err == nil && existing != nil {
		return nil, fmt.Errorf("app %q already exists", name)
	}

	kind := req.Kind
	if kind == "" {
		kind = store.KindWeb
	}
	env := req.Env
	if env == nil {
		env = map[string]string{}
	}

	destDir := filepath.Join(d.Opts.AppsRoot, "drops", id)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create drop dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(destDir) }

	if err := builder.ExtractArchive(req.ArchivePath, destDir); err != nil {
		cleanup()
		return nil, fmt.Errorf("extract: %w", err)
	}

	dropKind := ""
	var composeBytes []byte
	switch kind {
	case store.KindFunction:
		fb := builder.FunctionBuild{
			Language:   req.Runtime,
			Version:    req.RuntimeVersion,
			Entrypoint: req.Entrypoint,
			EnvKeys:    envKeys(env),
		}
		if err := builder.WriteFunctionBuild(destDir, fb); err != nil {
			cleanup()
			return nil, fmt.Errorf("function: %w", err)
		}
		composeBytes = []byte(fb.Compose())
	case store.KindCustom:
		if !builder.HasDockerfile(destDir) {
			cleanup()
			return nil, fmt.Errorf("custom deploy requires a Dockerfile in the archive")
		}
		dropKind = builder.DropKindDockerfile
		composeBytes = []byte(builder.SyntheticCompose())
	default:
		detected, err := builder.DetectKind(destDir)
		if err != nil {
			cleanup()
			return nil, err
		}
		dropKind = detected
		composeBytes = []byte(builder.SyntheticCompose())
		if detected == builder.DropKindStatic {
			baseHref := "/"
			if d.Opts.AppPathPrefix != "" {
				baseHref = strings.TrimRight(d.Opts.AppPathPrefix, "/") + "/" + name + "-web/"
			}
			if _, err := builder.WriteStaticFiles(destDir, baseHref); err != nil {
				cleanup()
				return nil, fmt.Errorf("static files: %w", err)
			}
		}
	}

	app := &store.App{
		ID:             id,
		Name:           name,
		Slug:           name,
		SourceType:     "drop",
		SourceRef:      req.SourceRef,
		DropKind:       dropKind,
		ComposeYAML:    string(composeBytes),
		Env:            env,
		Status:         "pending",
		Kind:           kind,
		Runtime:        req.Runtime,
		RuntimeVersion: req.RuntimeVersion,
		Entrypoint:     req.Entrypoint,
		ScaleToZero:    req.ScaleToZero,
		IdleTimeout:    req.IdleTimeout,
	}
	if kind == store.KindFunction {
		app.ListenPort = builder.DefaultFunctionPort
	}
	if err := d.Store.CreateApp(ctx, app); err != nil {
		cleanup()
		return nil, err
	}
	for k, v := range req.Secrets {
		if err := d.Store.SetSecret(ctx, id, k, v); err != nil {
			return nil, fmt.Errorf("store secret %q: %w", k, err)
		}
	}
	return app, nil
}
