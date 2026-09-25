// Package deployer holds the shared app deployment pipeline used by
// the web dashboard, the CLI, and the REST API. It exists so there is
// one implementation of clone→build→run→route instead of three
// drifting copies.
package deployer

import (
	"context"
	"fmt"
	"net"
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
	// The dashboard owns space-elevator.yml in the shared Traefik
	// directory; allowing an app with this name would overwrite the
	// dashboard route during publication.
	return name != "space-elevator" && appNamePattern.MatchString(name)
}

// Options carries the configuration the deployer can't derive itself.
type Options struct {
	AppsRoot        string
	BackupDir       string
	PodmanSocket    string
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
	if opts.BackupDir == "" {
		opts.BackupDir = filepath.Join(opts.AppsRoot, "backups")
	}
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
	// StaticBuild builds a web app and serves its selected output directory
	// with the platform's Nginx image. It is mutually exclusive with Build.
	StaticBuild *builder.StaticBuild
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
	return d.deployGit(ctx, req, audit.ActionAppDeploy, nil)
}

// DeployGitClaimed is the asynchronous web/API form. The caller claims
// the durable operation before returning its HTTP response so the
// deployment screen has a stable ID immediately.
func (d *Deployer) DeployGitClaimed(ctx context.Context, req GitRequest, operation *store.AppOperation) (*store.App, error) {
	return d.deployGit(ctx, req, audit.ActionAppDeploy, operation)
}

// RedeployGitClaimed retries the complete git pipeline for an existing app,
// including an app whose first clone or compose resolution failed. It uses
// the redeploy action so the create-only name check does not reject a retry.
func (d *Deployer) RedeployGitClaimed(ctx context.Context, req GitRequest, operation *store.AppOperation) (*store.App, error) {
	return d.deployGit(ctx, req, audit.ActionAppRedeploy, operation)
}

// deployGit runs the pipeline and attributes the outcome to action
// ("app.deploy" for a fresh deploy, "app.redeploy" when healing an
// existing app through the full git pipeline).
func (d *Deployer) deployGit(ctx context.Context, req GitRequest, action string, claimed *store.AppOperation) (*store.App, error) {
	// Validate the raw URL first: NormalizeGitURL is a pass-through for
	// non-http(s) input, so normalising before the check would let file://
	// and bare local paths through to go-git.
	if err := builder.ValidateRemoteURL(req.RepoURL); err != nil {
		return nil, err
	}
	req.RepoURL = builder.NormalizeGitURL(req.RepoURL)
	if !ValidAppName(req.Name) {
		return nil, fmt.Errorf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", req.Name)
	}
	ref := req.Ref
	if ref == "" {
		ref = "main"
	}

	prev, _ := d.Store.GetAppByName(ctx, req.Name)
	// A normal deploy is deliberately create-only. The web wizard creates a
	// pending row before starting its worker, so that narrow state remains a
	// valid first-deploy hand-off; all other existing rows must use Update.
	if prev != nil && action != audit.ActionAppRedeploy && !(prev.Status == "pending" && prev.ComposeYAML == "") {
		return nil, fmt.Errorf("app %q already exists; use the update operation to replace its source", req.Name)
	}

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
	builderImage, buildCmd, runCmd, servePath, port := "", "", "", "", 0
	if req.StaticBuild != nil {
		if req.Build != nil {
			return nil, fmt.Errorf("static and custom build settings cannot be used together")
		}
		if err := req.StaticBuild.Validate(); err != nil {
			return nil, fmt.Errorf("static build: %w", err)
		}
		buildMode = store.BuildModeStatic
		builderImage, buildCmd, servePath, port = req.StaticBuild.BuilderImage, req.StaticBuild.BuildCommand, req.StaticBuild.ServePath, req.StaticBuild.ListenPort
	} else if req.Build != nil {
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
	currentReleaseID := ""
	if prev != nil {
		if prev.Slug != "" {
			slug = prev.Slug
		}
		currentReleaseID = prev.CurrentReleaseID
	}
	app := &store.App{
		ID:               appID,
		Name:             req.Name,
		Slug:             slug,
		SourceType:       "git",
		SourceRef:        req.RepoURL,
		GitRef:           ref,
		Env:              env,
		CurrentReleaseID: currentReleaseID,
		BuildMode:        buildMode,
		BuilderImage:     builderImage,
		BuildCommand:     buildCmd,
		RunCommand:       runCmd,
		ServePath:        servePath,
		ListenPort:       port,
		Kind:             kind,
		Runtime:          req.Runtime,
		RuntimeVersion:   req.RuntimeVersion,
		Entrypoint:       req.Entrypoint,
		ScaleToZero:      req.ScaleToZero,
		IdleTimeout:      req.IdleTimeout,
		Status:           "pending",
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

	operation := claimed
	if operation == nil {
		var claimErr error
		operation, claimErr = d.Store.ClaimAppOperation(ctx, appID, action)
		if claimErr != nil {
			return nil, claimErr
		}
	} else if operation.AppID != appID {
		return nil, fmt.Errorf("deploy operation belongs to another app")
	}
	operationFinished := false
	finishOperation := func(status, opErr string) {
		if operationFinished {
			return
		}
		if finishErr := d.Store.FinishOperation(context.Background(), operation.ID, status, opErr); finishErr != nil {
			d.Opts.Log("warning: finalize operation %s: %v", operation.ID, finishErr)
		}
		operationFinished = true
	}
	defer func() {
		if !operationFinished {
			finishOperation(store.OperationStatusFailed, "deploy interrupted")
		}
	}()

	var restoreDeploySecrets func() error
	fail := func(err error) (*store.App, error) {
		if restoreDeploySecrets != nil {
			if rollbackErr := restoreDeploySecrets(); rollbackErr != nil {
				err = fmt.Errorf("%w (secret rollback failed: %v)", err, rollbackErr)
			}
		}
		for _, secret := range req.Secrets {
			err = redactErrorValue(err, secret)
		}
		_ = d.Store.UpdateAppStatusErr(ctx, appID, "error", err.Error())
		finishOperation(store.OperationStatusFailed, err.Error())
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
	var oldSpec *composer.Spec
	oldSourceDir := ""
	if prev != nil {
		oldSpec = parseStoredSpec(prev.ComposeYAML)
		if current, currentErr := d.Store.GetCurrentRelease(ctx, appID); currentErr == nil {
			oldSpec = parseStoredSpec(current.ComposeYAML)
			oldSourceDir = current.SourcePath
		} else if sourceDir, sourceErr := store.AppSourceDir(d.Opts.AppsRoot, prev); sourceErr == nil {
			oldSourceDir = sourceDir
		}
	}
	storagePlan, err := d.planRuntimeStorage(ctx, app, oldSpec, oldSourceDir, spec)
	if err != nil {
		return fail(fmt.Errorf("storage preflight: %w", err))
	}

	app.ComposeYAML = string(composeBytes)
	if app.BuildMode == "" {
		app.BuildMode = store.BuildModeCompose
	}
	if err := d.Store.UpdateApp(ctx, app); err != nil {
		return fail(fmt.Errorf("persist compose: %w", err))
	}
	if len(req.Secrets) > 0 {
		previousSecrets, secretErr := d.Store.GetSecrets(ctx, appID)
		if secretErr != nil {
			return fail(secretErr)
		}
		snapshot := make(map[string]string, len(previousSecrets))
		for k, v := range previousSecrets {
			snapshot[k] = v
		}
		restoreDeploySecrets = func() error { return d.replaceSecrets(ctx, appID, snapshot) }
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
		Storage: storagePlan.Candidate,
		Kind:    app.Kind, ScaleToZero: app.ScaleToZero,
	}

	// Tear down the previous deployment, if any, before bringing the
	// new one up (same container names would collide).
	if prev != nil {
		d.Opts.Log("Tearing down previous deployment...")
		oldSpec := &composer.Spec{}
		if parsed, parseErr := composer.Parse([]byte(prev.ComposeYAML)); parseErr == nil {
			oldSpec = parsed
		}
		if removeErr := d.Runtime.Remove(ctx, meta, oldSpec); removeErr != nil {
			return fail(fmt.Errorf("remove previous deployment: %w", removeErr))
		}
	}

	d.Opts.Stage("deploy")
	d.Opts.Log("Deploying %s...", app.Name)
	if err := d.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
		// A failed start can leave a partially-created container set. Remove
		// the candidate before marking the app failed so Retry starts from a
		// clean runtime instead of inheriting a half-created deployment.
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, app)
		return fail(fmt.Errorf("deploy failed: %w", err))
	}
	d.Opts.Stage("verify")
	if err := verifyRuntime(ctx, d, app, spec); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, app)
		return fail(fmt.Errorf("verify release: %w", err))
	}
	if err := d.persistRuntimeStorage(ctx, app, storagePlan); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, app)
		return fail(fmt.Errorf("persist storage: %w", err))
	}
	// A redeploy re-materialises the *current* checkout in place; it is not
	// an immutable release, so it must not claim a `releases/<id>/<rid>`
	// SourcePath. Any current_release_id from a prior update is now stale —
	// its source and compose no longer describe what is running — so clear
	// it. GetCurrentRelease then reports "none", and a later update falls
	// back to AppSourceDir + the compose we just wrote, which is exactly the
	// live truth. Leaving the stale pointer here would make update roll back
	// to a release that is no longer deployed.
	if action == audit.ActionAppRedeploy && prev != nil && prev.CurrentReleaseID != "" {
		if err := d.Store.ClearCurrentRelease(ctx, appID); err != nil {
			_ = d.Runtime.Remove(ctx, meta, spec)
			_ = d.SyncRoute(ctx, app)
			return fail(fmt.Errorf("clear stale current release: %w", err))
		}
	}
	if err := d.Store.UpdateAppStatusErr(ctx, appID, "running", ""); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, app)
		return fail(fmt.Errorf("persist app status: %w", err))
	}

	d.Opts.Stage("route")
	if err := d.applyRoute(ctx, app, spec); err != nil {
		return fail(fmt.Errorf("publish route: %w", err))
	}
	restoreDeploySecrets = nil
	finishOperation(store.OperationStatusCompleted, "")
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
	if app.BuildMode == store.BuildModeStatic {
		sb := builder.StaticBuild{
			BuilderImage: app.BuilderImage,
			BuildCommand: app.BuildCommand,
			ServePath:    app.ServePath,
			ListenPort:   app.ListenPort,
			BaseHref:     d.staticBaseHref(ctx, app),
			EnvKeys:      envKeys(app.Env),
		}
		if err := builder.WriteStaticBuild(sourceDir, sb); err != nil {
			return nil, fmt.Errorf("static web build: %w", err)
		}
		return []byte(sb.Compose()), nil
	}
	if app.BuildMode != store.BuildModeCustom {
		return nil, fmt.Errorf("repo has no compose file and no build settings: add a compose.yml, or configure a server/static web build")
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

func (d *Deployer) staticBaseHref(ctx context.Context, app *store.App) string {
	if len(domainsForSync(ctx, d, app)) > 0 || d.Opts.AppPathPrefix == "" {
		return "/"
	}
	return strings.TrimRight(d.Opts.AppPathPrefix, "/") + "/" + app.Slug + "/"
}

func (d *Deployer) currentSourceDir(ctx context.Context, a *store.App) (string, error) {
	if current, err := d.Store.GetCurrentRelease(ctx, a.ID); err == nil && current.SourcePath != "" {
		if safePath, safeErr := d.safeReleaseSource(a.ID, current.SourcePath); safeErr == nil {
			return safePath, nil
		} else {
			return "", safeErr
		}
	}
	return store.AppSourceDir(d.Opts.AppsRoot, a)
}

// planRuntimeStorage makes persisted/discovered logical storage available to
// every deploy path, not only the newer update transaction. This keeps a
// later redeploy on the same physical volumes/bind paths.
func (d *Deployer) planRuntimeStorage(ctx context.Context, app *store.App, oldSpec *composer.Spec, oldSourceDir string, spec *composer.Spec) (*storagePlan, error) {
	existing, err := d.Store.ListAppStorage(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	return d.planStorage(ctx, app, oldSpec, oldSourceDir, spec, existing)
}

func (d *Deployer) persistRuntimeStorage(ctx context.Context, app *store.App, plan *storagePlan) error {
	if plan == nil {
		return nil
	}
	for logical, binding := range plan.Candidate {
		if err := d.Store.UpsertAppStorage(ctx, &store.AppStorage{
			AppID: app.ID, LogicalName: logical,
			StorageKind: binding.Kind, PhysicalRef: binding.Ref,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Redeploy tears down and re-deploys an app from its on-disk source,
// regenerating synthesized files (static drops, custom builds) from
// current stored settings. Synchronous; used by the CLI directly and
// wrapped in a goroutine by web/API.
func (d *Deployer) Redeploy(ctx context.Context, a *store.App) error {
	operation, err := d.Store.ClaimAppOperation(ctx, a.ID, audit.ActionAppRedeploy)
	if err != nil {
		return err
	}
	return d.redeploy(ctx, a, operation)
}

// RedeployClaimed is used by asynchronous web/API callers that claim the
// per-app operation before returning their HTTP response.
func (d *Deployer) RedeployClaimed(ctx context.Context, a *store.App, operation *store.AppOperation) error {
	return d.redeploy(ctx, a, operation)
}

func (d *Deployer) redeploy(ctx context.Context, a *store.App, operation *store.AppOperation) (err error) {
	if operation == nil {
		return fmt.Errorf("redeploy operation is required")
	}
	finished := false
	finish := func(status string) {
		if finished {
			return
		}
		if finishErr := d.Store.FinishOperation(context.Background(), operation.ID, status, ""); finishErr != nil {
			d.Opts.Log("warning: finalize operation %s: %v", operation.ID, finishErr)
		}
		finished = true
	}
	defer func() {
		if err != nil {
			finish(store.OperationStatusFailed)
		} else {
			finish(store.OperationStatusCompleted)
		}
	}()
	// Git apps can heal from a failed or in-progress first deploy: if
	// the stored compose is unusable or the checkout is gone, the only
	// fix is the full clone→build pipeline from the stored repo/ref.
	if a.SourceType == "git" {
		sourceDir, _ := d.currentSourceDir(ctx, a)
		_, parseErr := composer.Parse([]byte(a.ComposeYAML))
		_, statErr := os.Lstat(sourceDir)
		if parseErr != nil || statErr != nil {
			d.Opts.Log("stored compose unusable for %s (%v); re-running the git pipeline", a.Name, parseErr)
			ref := a.GitRef
			if ref == "" {
				ref = "main"
			}
			var build *builder.CustomBuild
			var staticBuild *builder.StaticBuild
			if a.BuildMode == store.BuildModeCustom {
				build = &builder.CustomBuild{
					BuilderImage: a.BuilderImage,
					BuildCommand: a.BuildCommand,
					RunCommand:   a.RunCommand,
					ListenPort:   a.ListenPort,
				}
			} else if a.BuildMode == store.BuildModeStatic {
				staticBuild = &builder.StaticBuild{
					BuilderImage: a.BuilderImage,
					BuildCommand: a.BuildCommand,
					ServePath:    a.ServePath,
					ListenPort:   a.ListenPort,
				}
			}
			_, err := d.deployGit(ctx, GitRequest{
				Name:           a.Name,
				RepoURL:        a.SourceRef,
				Ref:            ref,
				Env:            a.Env,
				Build:          build,
				StaticBuild:    staticBuild,
				Kind:           a.Kind,
				Runtime:        a.Runtime,
				RuntimeVersion: a.RuntimeVersion,
				Entrypoint:     a.Entrypoint,
				ScaleToZero:    a.ScaleToZero,
				IdleTimeout:    a.IdleTimeout,
			}, audit.ActionAppRedeploy, operation)
			return err
		}
	}

	// Mark the row as deploying up front so UI progress views trigger
	// immediately (web/API run Redeploy in a goroutine); a fresh deploy
	// also clears the previous error display.
	if err := d.Store.UpdateAppStatusErr(ctx, a.ID, "pending", ""); err != nil {
		return err
	}

	sourceDir, err := d.currentSourceDir(ctx, a)
	if err != nil {
		return d.recordRedeployError(ctx, a, err)
	}
	if info, statErr := os.Lstat(sourceDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if statErr != nil {
			return d.recordRedeployError(ctx, a, fmt.Errorf("source directory missing: %w", statErr))
		}
		return d.recordRedeployError(ctx, a, fmt.Errorf("source directory is not a regular directory"))
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		return d.recordRedeployError(ctx, a, fmt.Errorf("stored compose is invalid: %w", err))
	}
	storagePlan, err := d.planRuntimeStorage(ctx, a, spec, sourceDir, spec)
	if err != nil {
		return d.recordRedeployError(ctx, a, fmt.Errorf("storage preflight: %w", err))
	}

	// Regenerate synthesized files so redeploys pick up latest synth
	// logic and edited build settings.
	if a.SourceType == "drop" && a.DropKind == "static" {
		domains, _ := d.Store.GetAppDomains(ctx, a.ID)
		baseHref := "/"
		if len(domains) == 0 && d.Opts.AppPathPrefix != "" {
			baseHref = strings.TrimRight(d.Opts.AppPathPrefix, "/") + "/" + a.Slug + "/"
		}
		if _, err := builder.WriteStaticFiles(sourceDir, baseHref); err != nil {
			return fmt.Errorf("regenerate static files: %w", err)
		}
	}
	if a.BuildMode == store.BuildModeStatic {
		sb := builder.StaticBuild{
			BuilderImage: a.BuilderImage,
			BuildCommand: a.BuildCommand,
			ServePath:    a.ServePath,
			ListenPort:   a.ListenPort,
			BaseHref:     d.staticBaseHref(ctx, a),
			EnvKeys:      envKeys(a.Env),
		}
		if err := builder.WriteStaticBuild(sourceDir, sb); err != nil {
			return fmt.Errorf("static build settings invalid: %w", err)
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
		Storage:     storagePlan.Candidate,
		StaticDrop:  a.SourceType == "drop" && a.DropKind == "static",
		Kind:        a.Kind,
		ScaleToZero: a.ScaleToZero,
	}

	d.Opts.Log("Tearing down previous deployment for %s...", a.Name)
	if err := d.Runtime.Remove(ctx, meta, spec); err != nil {
		return d.recordRedeployError(ctx, a, fmt.Errorf("remove previous deployment: %w", err))
	}
	d.Opts.Stage("deploy")
	d.Opts.Log("Re-deploying %s...", a.Name)
	if err := d.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, a)
		_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", "deploy failed: "+err.Error())
		d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
		return err
	}
	d.Opts.Stage("verify")
	if err := verifyRuntime(ctx, d, a, spec); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.SyncRoute(ctx, a)
		_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", "verify release: "+err.Error())
		d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
		return err
	}
	if err := d.persistRuntimeStorage(ctx, a, storagePlan); err != nil {
		_ = d.Runtime.Remove(ctx, meta, spec)
		_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", "persist storage: "+err.Error())
		d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
		return err
	}
	if err := d.Store.UpdateAppStatusErr(ctx, a.ID, "running", ""); err != nil {
		return err
	}
	d.Opts.Stage("route")
	if err := d.applyRoute(ctx, a, spec); err != nil {
		_ = d.Store.UpdateAppStatusErr(ctx, a.ID, "error", "publish route: "+err.Error())
		d.audit(ctx, audit.ActionAppRedeploy, a, audit.OutcomeFailure, err.Error())
		return err
	}
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
// container state. A route publication failure is part of deployment
// correctness: silently leaving an old route can expose the wrong release.
func (d *Deployer) applyRoute(ctx context.Context, a *store.App, spec *composer.Spec) error {
	if err := d.SyncRoute(ctx, a); err != nil {
		if d.Traefik != nil && a != nil {
			_ = d.Traefik.Remove(a.Slug)
		}
		return err
	}
	d.Opts.Log("Traefik route written for %s.", a.Name)
	return nil
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
		if d.Traefik != nil {
			appName := a.Slug
			if appName == "" {
				appName = a.Name
			}
			_ = d.Traefik.Remove(appName)
		}
		return fmt.Errorf("stored compose is invalid: %w", err)
	}
	domains, err := d.Store.GetAppDomains(ctx, a.ID)
	if err != nil {
		return err
	}
	appName := a.Slug
	if appName == "" {
		appName = a.Name
	}
	_, err = traefik.ApplyAppRoute(ctx, traefik.AppOptions{
		Writer:          d.Traefik,
		Client:          d.Client,
		AppName:         appName,
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
	credentialHost := strings.ToLower(host.Host)
	if parsedHost, _, splitErr := net.SplitHostPort(credentialHost); splitErr == nil && parsedHost != "" {
		credentialHost = parsedHost
	}
	auth := &builder.Auth{}
	if cred, err := d.Store.GetCredentialForHost(ctx, credentialHost); err == nil {
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
