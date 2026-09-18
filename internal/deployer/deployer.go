// Package deployer holds the shared app deployment pipeline used by
// the web dashboard, the CLI, and the REST API. It exists so there is
// one implementation of clone→build→run→route instead of three
// drifting copies.
package deployer

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

// appNamePattern matches URL/hostname/container-safe app names.
// Trailing hyphens are disallowed so names can't be glued into double
// dashes in downstream keys (image tags, Traefik router names).
var appNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidAppName enforces URL/hostname/container-safe names up front, so
// nothing downstream (paths, Traefik keys, redirects) ever sees junk.
func ValidAppName(name string) bool {
	return appNamePattern.MatchString(name)
}

// Options carries the configuration the deployer can't derive itself.
type Options struct {
	AppsRoot        string
	PublicHost      string
	AppPathPrefix   string
	RootlessGateway string
	CertResolver    string
	// Log receives progress lines; CLI prints them, web/API discard.
	Log func(format string, args ...any)
	// Stage reports coarse pipeline transitions (clone, compose,
	// deploy, route) so UIs can drive a progress indicator. Nil-safe:
	// New installs a no-op.
	Stage func(stage string)
	// Audit records deploy outcomes. Nil-safe: nil disables auditing.
	Audit *audit.Logger
}

// Deployer runs deploys and redeploys against a store + podman runtime.
type Deployer struct {
	Store   *store.Store
	Runtime *composer.Runtime
	Client  *podman.Client
	Traefik *traefik.Writer
	Opts    Options
}

func New(st *store.Store, rt *composer.Runtime, cli *podman.Client, tw *traefik.Writer, opts Options) *Deployer {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	if opts.Stage == nil {
		opts.Stage = func(string) {}
	}
	return &Deployer{Store: st, Runtime: rt, Client: cli, Traefik: tw, Opts: opts}
}

// GitRequest describes a deploy from a git source. Secrets are stored
// write-only; env/secrets reach containers via LoadRuntimeEnv at
// deploy time.
type GitRequest struct {
	Name    string
	RepoURL string
	Ref     string
	Env     map[string]string
	Secrets map[string]string
	// Build switches to custom build mode (synthesized Dockerfile)
	// when non-nil. A repo that ships its own compose file always
	// wins over these settings.
	Build *builder.CustomBuild
	// Kind selects the workload class. Empty means "web" (except that an
	// existing app's kind is preserved when redeploying). "function"
	// synthesizes an adapter-wrapped handler from Runtime/Entrypoint.
	Kind           string
	Runtime        string
	RuntimeVersion string
	Entrypoint     string
	// ScaleToZero/idle settings are stored now and applied by a future
	// activator; they also gate the container labels.
	ScaleToZero bool
	IdleTimeout int
}

// DeployGit runs the full pipeline synchronously:
//
//	create row (pending) → clone → compose-or-synth → persist →
//	secrets → teardown previous → deploy → traefik route
//
// Re-deploying over an existing app name replaces it (settings merge:
// stored env survives unless overridden in the request). Every failure
// is recorded on the app row before returning.
func (d *Deployer) DeployGit(ctx context.Context, req GitRequest) (*store.App, error) {
	return d.deployGit(ctx, req, audit.ActionAppDeploy)
}

// deployGit runs the pipeline and attributes the outcome to action
// ("app.deploy" for a fresh deploy, "app.redeploy" when healing an
// existing app through the full git pipeline).
func (d *Deployer) deployGit(ctx context.Context, req GitRequest, action string) (*store.App, error) {
	if !ValidAppName(req.Name) {
		return nil, fmt.Errorf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", req.Name)
	}
	ref := req.Ref
	if ref == "" {
		ref = "main"
	}

	prev, _ := d.Store.GetAppByName(ctx, req.Name)

	// One ID for the clone dir, the row, and every artifact.
	appID := uuid.NewString()
	if prev != nil {
		appID = prev.ID
	}

	// Merge request env over stored env; stored survives unless
	// overridden (matches CLI redeploy semantics).
	env := map[string]string{}
	if prev != nil {
		for k, v := range prev.Env {
			env[k] = v
		}
	}
	for k, v := range req.Env {
		env[k] = v
	}

	buildMode := store.BuildModeCompose
	builderImage, buildCmd, runCmd, port := "", "", "", 0
	if req.Build != nil {
		buildMode = store.BuildModeCustom
		builderImage, buildCmd, runCmd, port = req.Build.BuilderImage, req.Build.BuildCommand, req.Build.RunCommand, req.Build.ListenPort
	}

	// Kind: explicit request wins; otherwise preserve the stored kind on
	// redeploy; otherwise "web". Function fields are validated up front.
	kind := store.KindWeb
	if req.Kind != "" {
		kind = req.Kind
	} else if prev != nil && prev.Kind != "" {
		kind = prev.Kind
	}
	if kind == store.KindFunction {
		if err := (builder.FunctionBuild{
			Language: req.Runtime, Version: req.RuntimeVersion, Entrypoint: req.Entrypoint,
		}).Validate(); err != nil {
			return nil, fmt.Errorf("function: %w", err)
		}
	}

	// Re-deploying over an existing row keeps its runtime slug (which
	// may differ from the name after a rename); a fresh app's slug is
	// its name.
	slug := req.Name
	if prev != nil && prev.Slug != "" {
		slug = prev.Slug
	}
	app := &store.App{
		ID:             appID,
		Name:           req.Name,
		Slug:           slug,
		SourceType:     "git",
		SourceRef:      req.RepoURL,
		GitRef:         ref,
		Env:            env,
		BuildMode:      buildMode,
		BuilderImage:   builderImage,
		BuildCommand:   buildCmd,
		RunCommand:     runCmd,
		ListenPort:     port,
		Kind:           kind,
		Runtime:        req.Runtime,
		RuntimeVersion: req.RuntimeVersion,
		Entrypoint:     req.Entrypoint,
		ScaleToZero:    req.ScaleToZero,
		IdleTimeout:    req.IdleTimeout,
		Status:         "pending",
	}
	if kind == store.KindFunction {
		app.ListenPort = builder.DefaultFunctionPort
	}

	// Create/refresh the row up front so a failed clone is visible on
	// the app page instead of silently vanishing.
	if prev == nil {
		if err := d.Store.CreateApp(ctx, app); err != nil {
			return nil, fmt.Errorf("create app row: %w", err)
		}
	} else if err := d.Store.UpdateApp(ctx, app); err != nil {
		return nil, fmt.Errorf("update app row: %w", err)
	}

	fail := func(err error) (*store.App, error) {
		_ = d.Store.UpdateAppStatusErr(ctx, appID, "error", err.Error())
		d.audit(ctx, action, app, audit.OutcomeFailure, err.Error())
		return nil, err
	}

	d.Opts.Stage("clone")
	d.Opts.Log("Cloning %s (%s)...", req.RepoURL, ref)
	auth := d.gitAuth(ctx, req.RepoURL)
	sourceDir := filepath.Join(d.Opts.AppsRoot, "sources", appID)
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		return fail(fmt.Errorf("create source dir: %w", err))
	}
	// A previous deploy of this app (or a failed attempt) leaves a
	// checkout behind; PlainClone refuses to clone into an existing
	// repository. Always start from a fresh clone.
	if err := os.RemoveAll(sourceDir); err != nil {
		return fail(fmt.Errorf("clear source dir: %w", err))
	}
	if err := builder.Clone(ctx, req.RepoURL, ref, sourceDir, auth); err != nil {
		return fail(fmt.Errorf("git clone: %w", err))
	}

	d.Opts.Stage("compose")
	composeBytes, err := d.resolveCompose(ctx, app, sourceDir)
	if err != nil {
		return fail(err)
	}
	spec, err := composer.Parse(composeBytes)
	if err != nil {
		return fail(fmt.Errorf("compose parse: %w", err))
	}

	app.ComposeYAML = string(composeBytes)
	if app.BuildMode == "" {
		app.BuildMode = store.BuildModeCompose
	}
	if err := d.Store.UpdateApp(ctx, app); err != nil {
		return fail(fmt.Errorf("persist compose: %w", err))
	}
	for k, v := range req.Secrets {
		if err := d.Store.SetSecret(ctx, appID, k, v); err != nil {
			return fail(fmt.Errorf("store secret %q: %w", k, err))
		}
	}

	runtimeEnv, err := d.Store.LoadRuntimeEnv(ctx, appID, app.Env)
	if err != nil {
		return fail(fmt.Errorf("load env: %w", err))
	}
	meta := composer.AppMeta{
		ID: appID, Name: app.Slug, Env: runtimeEnv, Label: app.Slug, BuildEnv: app.Env,
		Kind: app.Kind, ScaleToZero: app.ScaleToZero,
	}

	// Tear down the previous deployment, if any, before bringing the
	// new one up (same container names would collide).
	if prev != nil {
		d.Opts.Log("Tearing down previous deployment...")
		if oldSpec, err := composer.Parse([]byte(prev.ComposeYAML)); err == nil {
			if err := d.Runtime.Remove(ctx, meta, oldSpec); err != nil {
				d.Opts.Log("warn: remove previous: %v", err)
			}
		}
	}

	d.Opts.Stage("deploy")
	d.Opts.Log("Deploying %s...", app.Name)
	if err := d.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
		return fail(fmt.Errorf("deploy failed: %w", err))
	}
	if err := d.Store.UpdateAppStatusErr(ctx, appID, "running", ""); err != nil {
		return nil, err
	}

	d.Opts.Stage("route")
	d.applyRoute(ctx, app, spec)
	d.audit(ctx, action, app, audit.OutcomeSuccess, "source "+app.SourceType+" "+app.SourceRef+" ref "+app.GitRef)
	return app, nil
}

// resolveCompose finds the compose file in sourceDir or, in custom
// build mode, synthesizes Dockerfile + compose.yml there. Returns the
// compose bytes to parse and store.
func (d *Deployer) resolveCompose(ctx context.Context, app *store.App, sourceDir string) ([]byte, error) {
	// Functions always use the generated adapter, even if the repo also
	// ships a compose file.
	if app.Kind == store.KindFunction {
		fb := builder.FunctionBuild{
			Language:   app.Runtime,
			Version:    app.RuntimeVersion,
			Entrypoint: app.Entrypoint,
			EnvKeys:    envKeys(app.Env),
		}
		if err := builder.WriteFunctionBuild(sourceDir, fb); err != nil {
			return nil, fmt.Errorf("function build: %w", err)
		}
		return []byte(fb.Compose()), nil
	}
	composePath, composeErr := builder.FindComposeFile(sourceDir)
	if composeErr == nil {
		b, err := os.ReadFile(composePath)
		if err != nil {
			return nil, fmt.Errorf("read compose file: %w", err)
		}
		if _, err := composer.Parse(b); err != nil {
			return nil, fmt.Errorf("%s: %w — a compose file needs at least one entry under \"services:\"", filepath.Base(composePath), err)
		}
		// A repo that ships its own compose file wins over stale
		// custom-build settings.
		app.BuildMode = store.BuildModeCompose
		return b, nil
	}
	if app.BuildMode != store.BuildModeCustom {
		return nil, fmt.Errorf("repo has no compose file and no advanced build settings: add a compose.yml, or set a builder image + build/run commands")
	}
	cb := builder.CustomBuild{
		BuilderImage: app.BuilderImage,
		BuildCommand: app.BuildCommand,
		RunCommand:   app.RunCommand,
		ListenPort:   app.ListenPort,
		EnvKeys:      envKeys(app.Env),
	}
	if err := builder.WriteCustomBuild(sourceDir, cb); err != nil {
		return nil, fmt.Errorf("advanced deploy: %w", err)
	}
	return []byte(cb.Compose()), nil
}

// Redeploy tears down and re-deploys an app from its on-disk source,
// regenerating synthesized files (static drops, custom builds) from
// current stored settings. Synchronous; used by the CLI directly and
// wrapped in a goroutine by web/API.
func (d *Deployer) Redeploy(ctx context.Context, a *store.App) error {
	// Git apps can heal from a failed or in-progress first deploy: if
	// the stored compose is unusable or the checkout is gone, the only
	// fix is the full clone→build pipeline from the stored repo/ref.
	if a.SourceType == "git" {
		sourceDir, _ := store.AppSourceDir(d.Opts.AppsRoot, a)
		_, parseErr := composer.Parse([]byte(a.ComposeYAML))
		_, statErr := os.Stat(sourceDir)
		if parseErr != nil || statErr != nil {
			d.Opts.Log("stored compose unusable for %s (%v); re-running the git pipeline", a.Name, parseErr)
			ref := a.GitRef
			if ref == "" {
				ref = "main"
			}
			var build *builder.CustomBuild
			if a.BuildMode == store.BuildModeCustom {
				build = &builder.CustomBuild{
					BuilderImage: a.BuilderImage,
					BuildCommand: a.BuildCommand,
					RunCommand:   a.RunCommand,
					ListenPort:   a.ListenPort,
				}
			}
			_, err := d.deployGit(ctx, GitRequest{
				Name:           a.Name,
				RepoURL:        a.SourceRef,
				Ref:            ref,
				Env:            a.Env,
				Build:          build,
				Kind:           a.Kind,
				Runtime:        a.Runtime,
				RuntimeVersion: a.RuntimeVersion,
				Entrypoint:     a.Entrypoint,
				ScaleToZero:    a.ScaleToZero,
				IdleTimeout:    a.IdleTimeout,
			}, audit.ActionAppRedeploy)
			return err
		}
	}

	// Mark the row as deploying up front so UI progress views trigger
	// immediately (web/API run Redeploy in a goroutine); a fresh deploy
	// also clears the previous error display.
	if err := d.Store.UpdateAppStatusErr(ctx, a.ID, "pending", ""); err != nil {
		return err
	}

	sourceDir, err := store.AppSourceDir(d.Opts.AppsRoot, a)
	if err != nil {
		return d.recordRedeployError(ctx, a, err)
	}
	if _, err := os.Stat(sourceDir); err != nil {
		return d.recordRedeployError(ctx, a, fmt.Errorf("source directory missing; deploy again from the original source"))
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		return d.recordRedeployError(ctx, a, fmt.Errorf("stored compose is invalid: %w", err))
	}

	// Regenerate synthesized files so redeploys pick up latest synth
	// logic and edited build settings.
	if a.SourceType == "drop" && a.DropKind == "static" {
		domains, _ := d.Store.GetAppDomains(ctx, a.ID)
		baseHref := "/"
		if len(domains) == 0 && d.Opts.AppPathPrefix != "" {
			baseHref = strings.TrimRight(d.Opts.AppPathPrefix, "/") + "/" + a.Slug + "-web/"
		}
		if _, err := builder.WriteStaticFiles(sourceDir, baseHref); err != nil {
			return fmt.Errorf("regenerate static files: %w", err)
		}
	}
	if a.Kind == store.KindFunction {
		fb := builder.FunctionBuild{
			Language:   a.Runtime,
			Version:    a.RuntimeVersion,
			Entrypoint: a.Entrypoint,
			EnvKeys:    envKeys(a.Env),
		}
		if err := builder.WriteFunctionBuild(sourceDir, fb); err != nil {
			return fmt.Errorf("function build settings invalid: %w", err)
		}
	}
	if a.BuildMode == store.BuildModeCustom {
		cb := builder.CustomBuild{
			BuilderImage: a.BuilderImage,
			BuildCommand: a.BuildCommand,
			RunCommand:   a.RunCommand,
			ListenPort:   a.ListenPort,
			EnvKeys:      envKeys(a.Env),
		}
		if err := builder.WriteCustomBuild(sourceDir, cb); err != nil {
			return fmt.Errorf("custom build settings invalid: %w", err)
		}
	}

	runtimeEnv, err := d.Store.LoadRuntimeEnv(ctx, a.ID, a.Env)
	if err != nil {
		return fmt.Errorf("load env: %w", err)
	}
	meta := composer.AppMeta{
		ID:          a.ID,
		Name:        a.Slug,
		Env:         runtimeEnv,
		Label:       a.Slug,
		BuildEnv:    a.Env,
		StaticDrop:  a.SourceType == "drop" && a.DropKind == "static",
		Kind:        a.Kind,
		ScaleToZero: a.ScaleToZero,
	}

	d.Opts.Log("Tearing down previous deployment for %s...", a.Name)
	if err := d.Runtime.Remove(ctx, meta, spec); err != nil {
		d.Opts.Log("warn: remove: %v", err)
	}
	d.Opts.Stage("deploy")
	d.Opts.Log("Re-deploying %s...", a.Name)
	if err := d.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
		_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", "deploy failed: "+err.Error())
		d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
		return err
	}
	if err := d.Store.UpdateAppStatusErr(ctx, a.ID, "running", ""); err != nil {
		return err
	}
	d.Opts.Stage("route")
	d.applyRoute(ctx, a, spec)
	d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeSuccess, "source "+a.SourceType+" "+a.SourceRef)
	return nil
}

// recordRedeployError surfaces pre-deploy failures on the app row so
// web/API users (who run Redeploy in a goroutine) see why nothing
// happened instead of a silently swallowed error.
func (d *Deployer) recordRedeployError(ctx context.Context, a *store.App, err error) error {
	_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", err.Error())
	d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
	return err
}

// audit records a deploy outcome. Nil-safe and best-effort; the actor,
// IP, and User-Agent are read from ctx by the audit logger.
func (d *Deployer) audit(ctx context.Context, action string, app *store.App, outcome, detail string) {
	if d.Opts.Audit == nil || app == nil {
		return
	}
	d.Opts.Audit.Record(ctx, audit.Event{
		Action:     action,
		TargetType: "app",
		TargetID:   app.ID,
		TargetName: app.Name,
		Outcome:    outcome,
		Detail:     detail,
	})
}

// applyRoute rewrites the app's Traefik dynamic file from current
// container state; failures are logged, not fatal (the app is up).
func (d *Deployer) applyRoute(ctx context.Context, a *store.App, spec *composer.Spec) {
	if err := d.SyncRoute(ctx, a); err != nil {
		d.Opts.Log("warn: traefik: %v", err)
		return
	}
	d.Opts.Log("Traefik route written for %s.", a.Name)
}

// SyncRoute makes the app's Traefik dynamic file match its attached
// domains and current containers. When the app has no exposure
// configured, or its containers are not running, ApplyAppRoute removes
// any existing file so Traefik never keeps routing to a dead backend
// (502) or a live backend with no route (404). Callers on start, stop,
// restart, and startup must keep routing in sync this way; the plain
// Runtime.Start/Stop helpers do not touch Traefik.
func (d *Deployer) SyncRoute(ctx context.Context, a *store.App) error {
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		return fmt.Errorf("stored compose is invalid: %w", err)
	}
	domains, err := d.Store.GetAppDomains(ctx, a.ID)
	if err != nil {
		return err
	}
	_, err = traefik.ApplyAppRoute(ctx, traefik.AppOptions{
		Writer:          d.Traefik,
		Client:          d.Client,
		AppName:         a.Slug,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      d.Opts.PublicHost,
		AppPathPrefix:   d.Opts.AppPathPrefix,
		RootlessGateway: d.Opts.RootlessGateway,
	})
	return err
}

func (d *Deployer) gitAuth(ctx context.Context, repoURL string) *builder.Auth {
	host, _ := url.Parse(repoURL)
	if host == nil || host.Host == "" {
		return &builder.Auth{}
	}
	auth := &builder.Auth{}
	if cred, err := d.Store.GetCredentialForHost(ctx, host.Host); err == nil {
		auth.Username = cred.Username
		auth.Token = cred.Token
	}
	return auth
}

// envKeys returns the sorted key names of the app's plain env, used to
// declare ARG lines in synthesized Dockerfiles so --build-arg values
// reach the build command.
func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
