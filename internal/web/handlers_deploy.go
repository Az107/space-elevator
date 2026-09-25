package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type deployFormValues struct {
	Kind           string
	Source         string
	BuildMode      string
	URL            string
	Ref            string
	Image          string
	Port           string
	BuildCommand   string
	RunCommand     string
	ServePath      string
	Language       string
	RuntimeVersion string
	Entrypoint     string
	Name           string
	Env            string
}

func defaultDeployFormValues() deployFormValues {
	return deployFormValues{
		Kind: store.KindWeb, Source: "git", BuildMode: store.BuildModeCompose,
		Ref: "main", Language: "python",
	}
}

func deployFormValuesFromRequest(r *http.Request) deployFormValues {
	values := defaultDeployFormValues()
	values.Kind = strings.TrimSpace(r.FormValue("kind"))
	values.Source = strings.TrimSpace(r.FormValue("source"))
	values.BuildMode = strings.TrimSpace(r.FormValue("build_mode"))
	values.URL = r.FormValue("url")
	values.Ref = strings.TrimSpace(r.FormValue("ref"))
	values.Image = r.FormValue("image")
	values.Port = r.FormValue("port")
	values.BuildCommand = r.FormValue("build_cmd")
	values.RunCommand = r.FormValue("run_cmd")
	values.ServePath = r.FormValue("serve_path")
	values.Language = r.FormValue("language")
	values.RuntimeVersion = r.FormValue("runtime_version")
	values.Entrypoint = r.FormValue("entrypoint")
	values.Name = r.FormValue("name")
	values.Env = r.FormValue("env")
	if values.Kind == "" {
		values.Kind = store.KindWeb
	}
	if values.Source == "" {
		values.Source = "git"
	}
	if values.BuildMode == "" {
		values.BuildMode = store.BuildModeCompose
	}
	if values.Ref == "" {
		values.Ref = "main"
	}
	if values.Language == "" {
		values.Language = "python"
	}
	return values
}

type deployFormData struct {
	PageData
	Error  string
	Values deployFormValues
}

// deployTimeout bounds a full clone+build+run deploy so a stuck git
// server or image build can't wedge a worker forever.
const deployTimeout = 30 * time.Minute

// validAppName delegates to the shared deployer validator so dashboard,
// API, and CLI cannot drift on reserved or unsafe names.
func validAppName(name string) bool {
	return deployer.ValidAppName(name)
}

func (s *Server) handleDeployForm(w http.ResponseWriter, r *http.Request) {
	s.Renderer.Render(w, r, "deploy.html", deployFormData{PageData: pageCtx(r, "New app"), Values: defaultDeployFormValues()})
}

// handleDeploySubmit is the unified wizard endpoint. It dispatches on
// the "source" field: git deploys synchronously create a row and kick
// off the clone pipeline; upload deploys extract an archive and do the
// same, sharing CreateUpload with the dropzone and CLI/API.
func (s *Server) handleDeploySubmit(w http.ResponseWriter, r *http.Request) {
	values := deployFormValuesFromRequest(r)
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "New app"), Values: values,
			Error: "Could not parse form: " + err.Error(),
		})
		return
	}
	// ParseMultipartForm may have populated additional values that were not
	// available before parsing (notably multipart text fields).
	values = deployFormValuesFromRequest(r)
	fail := func(msg string) {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "New app"), Values: values, Error: msg,
		})
	}

	source := r.FormValue("source")
	if source == "" {
		source = "git"
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		kind = store.KindWeb
	}
	switch kind {
	case store.KindWeb, store.KindFunction, store.KindCustom:
	default:
		fail(fmt.Sprintf("Unknown app kind %q.", kind))
		return
	}

	if source == "upload" {
		app, err := s.createUploadFromRequest(r)
		if err != nil {
			fail(err.Error())
			return
		}
		s.recordAudit(r, audit.ActionAppUpload, "app", app.ID, app.Name, audit.OutcomeSuccess, "archive "+app.SourceRef)
		operation, opErr := s.Store.ClaimAppOperation(r.Context(), app.ID, audit.ActionAppDeploy)
		if opErr != nil {
			_ = s.deleteApp(r.Context(), app)
			fail("Could not queue deployment: " + opErr.Error())
			return
		}
		s.startUploadDeploy(s.backgroundAuditCtx(r), app, operation)
		http.Redirect(w, r, "/deployments/"+operation.ID, http.StatusSeeOther)
		return
	}

	rawRepoURL := strings.TrimSpace(r.FormValue("url"))
	repoURL := builder.NormalizeGitURL(rawRepoURL)
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	ref := strings.TrimSpace(r.FormValue("ref"))
	if ref == "" {
		ref = "main"
	}
	if webRef, ok := builder.RefFromWebURL(rawRepoURL); ok && (ref == "" || ref == "main") {
		ref = webRef
	}
	env, envBad := store.ParseKVLines(r.FormValue("env"))
	secrets, secretBad := store.ParseKVLines(r.FormValue("secrets"))

	// Function fields (only meaningful for kind=function).
	fb := builder.FunctionBuild{
		Language:   r.FormValue("language"),
		Version:    r.FormValue("runtime_version"),
		Entrypoint: r.FormValue("entrypoint"),
	}
	if kind == store.KindFunction {
		if err := fb.Validate(); err != nil {
			fail("Function: " + err.Error() + ".")
			return
		}
	}

	// Web apps can use the repository's compose file, a command-based
	// custom image, or the first-class static builder.
	builderImage := strings.TrimSpace(r.FormValue("image"))
	buildCmd := strings.TrimSpace(r.FormValue("build_cmd"))
	runCmd := strings.TrimSpace(r.FormValue("run_cmd"))
	servePath := strings.TrimSpace(r.FormValue("serve_path"))
	buildMode := strings.TrimSpace(r.FormValue("build_mode"))
	staticBuild := kind == store.KindWeb && buildMode == store.BuildModeStatic
	custom := kind != store.KindFunction && !staticBuild && (buildMode == store.BuildModeCustom || builderImage != "" || runCmd != "" || buildCmd != "")
	port := 0
	var portErr error
	if staticBuild {
		if r.FormValue("port") == "" {
			port = builder.DefaultStaticListenPort
		} else {
			port, portErr = builder.ParsePort(r.FormValue("port"))
		}
		if builderImage == "" {
			builderImage = "node:20-bookworm"
		}
		sb := builder.StaticBuild{BuilderImage: builderImage, BuildCommand: buildCmd, ServePath: servePath, ListenPort: port}
		if err := sb.Validate(); err != nil {
			fail("Static web build: " + err.Error() + ".")
			return
		}
	} else {
		port, portErr = builder.ParsePort(r.FormValue("port"))
		if custom {
			cb := builder.CustomBuild{BuilderImage: builderImage, BuildCommand: buildCmd, RunCommand: runCmd, ListenPort: port}
			if err := cb.Validate(); err != nil {
				fail("Advanced deploy: " + err.Error() + ".")
				return
			}
		} else if portErr == nil && r.FormValue("port") != "" {
			// A port with no build settings means the user half-filled the
			// advanced section; surface it instead of silently ignoring.
			portErr = errors.New("builder image or run command required when setting a port")
		}
	}
	if portErr != nil {
		fail("Advanced deploy: " + portErr.Error() + ".")
		return
	}

	if name == "" {
		name = strings.ToLower(nameFromURL(repoURL))
	}
	if repoURL == "" || name == "" {
		fail("URL and name required.")
		return
	}
	if !validAppName(name) {
		fail(fmt.Sprintf("Invalid app name %q: use 1-63 lowercase letters, digits, or hyphens (leading character must be a letter or digit).", name))
		return
	}
	if len(envBad) > 0 || len(secretBad) > 0 {
		bad := append(append([]string{}, envBad...), secretBad...)
		fail(fmt.Sprintf("Invalid environment/secret line(s) %q: use KEY=VALUE per line; keys must match [A-Za-z_][A-Za-z0-9_]*.", bad))
		return
	}
	if _, err := s.Store.GetAppByName(r.Context(), name); err == nil {
		fail(fmt.Sprintf("App %q already exists; remove it first or use a different name.", name))
		return
	}

	// Create the row up front so the redirect target exists
	// immediately and failures are visible on the detail page.
	// DeployGit detects the pending row and takes the update path;
	// secrets are stored there (values never touch the row).
	appID := uuid.NewString()
	appBuildMode := store.BuildModeCompose
	if custom {
		appBuildMode = store.BuildModeCustom
	} else if staticBuild {
		appBuildMode = store.BuildModeStatic
	}
	app := &store.App{
		ID:             appID,
		Name:           name,
		Slug:           name,
		SourceType:     "git",
		SourceRef:      repoURL,
		GitRef:         ref,
		Env:            env,
		BuildMode:      appBuildMode,
		BuilderImage:   builderImage,
		BuildCommand:   buildCmd,
		RunCommand:     runCmd,
		ServePath:      servePath,
		ListenPort:     port,
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		ScaleToZero:    r.FormValue("scale_to_zero") != "",
		IdleTimeout:    atoiDefault(r.FormValue("idle_timeout"), 0),
		Status:         "pending",
	}
	if kind == store.KindFunction {
		app.ListenPort = builder.DefaultFunctionPort
	}
	if err := s.Store.CreateApp(r.Context(), app); err != nil {
		fail(fmt.Sprintf("Could not create app %q: %v.", name, err))
		return
	}
	s.recordAudit(r, audit.ActionAppCreate, "app", app.ID, app.Name, audit.OutcomeSuccess, "git "+repoURL+" ref "+ref)

	operation, operationErr := s.Store.ClaimAppOperation(r.Context(), app.ID, audit.ActionAppDeploy)
	if operationErr != nil {
		_ = s.deleteApp(r.Context(), app)
		fail("Could not queue deployment: " + operationErr.Error())
		return
	}
	for k, v := range secrets {
		if err := s.Store.SetSecret(r.Context(), app.ID, k, v); err != nil {
			_ = s.deleteApp(r.Context(), app)
			fail("Could not save deployment secrets: " + err.Error())
			return
		}
	}

	var buildReq *builder.CustomBuild
	var staticReq *builder.StaticBuild
	if custom {
		buildReq = &builder.CustomBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			RunCommand:   runCmd,
			ListenPort:   port,
		}
	} else if staticBuild {
		staticReq = &builder.StaticBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			ServePath:    servePath,
			ListenPort:   port,
		}
	}
	s.submitDeployment(func() {
		s.deployAsync(s.backgroundAuditCtx(r), deployer.GitRequest{
			Name:           name,
			RepoURL:        repoURL,
			Ref:            ref,
			Env:            env,
			Secrets:        secrets,
			Build:          buildReq,
			StaticBuild:    staticReq,
			Kind:           kind,
			Runtime:        fb.Language,
			RuntimeVersion: fb.Version,
			Entrypoint:     fb.Entrypoint,
			ScaleToZero:    app.ScaleToZero,
			IdleTimeout:    app.IdleTimeout,
		}, operation)
	})
	http.Redirect(w, r, "/deployments/"+operation.ID, http.StatusSeeOther)
}

// createUploadFromRequest extracts the multipart archive and common
// fields from an upload request (wizard or dropzone), creates the
// pending app row, and returns it. It does not start the deploy.
func (s *Server) createUploadFromRequest(r *http.Request) (*store.App, error) {
	f, header, err := r.FormFile("tarball")
	if err != nil {
		return nil, errors.New("missing 'tarball' file field — drag a single .tar.gz / .zip file, not a folder")
	}
	defer f.Close()
	if header.Size == 0 {
		return nil, errors.New("uploaded file is empty")
	}

	suffix := ""
	lower := strings.ToLower(header.Filename)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		suffix = ".zip"
	case strings.HasSuffix(lower, ".tar.gz"):
		suffix = ".tar.gz"
	case strings.HasSuffix(lower, ".tgz"):
		suffix = ".tgz"
	}
	tmp, err := os.CreateTemp("", "se-upload-*"+suffix)
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	env, envBad := store.ParseKVLines(r.FormValue("env"))
	secrets, secretBad := store.ParseKVLines(r.FormValue("secrets"))
	if len(envBad) > 0 || len(secretBad) > 0 {
		bad := append(append([]string{}, envBad...), secretBad...)
		return nil, fmt.Errorf("invalid environment/secret line(s) %q: use KEY=VALUE per line; keys must match [A-Za-z_][A-Za-z0-9_]*.", bad)
	}

	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		kind = store.KindWeb
	}
	switch kind {
	case store.KindWeb, store.KindFunction, store.KindCustom:
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	fb := builder.FunctionBuild{
		Language:   r.FormValue("language"),
		Version:    r.FormValue("runtime_version"),
		Entrypoint: r.FormValue("entrypoint"),
	}
	if kind == store.KindFunction {
		if err := fb.Validate(); err != nil {
			return nil, fmt.Errorf("function: %w", err)
		}
	}

	return s.Deployer.CreateUpload(r.Context(), deployer.UploadRequest{
		Name:           strings.ToLower(strings.TrimSpace(r.FormValue("name"))),
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		SourceRef:      header.Filename,
		Env:            env,
		Secrets:        secrets,
		ArchivePath:    tmpPath,
		ScaleToZero:    r.FormValue("scale_to_zero") != "",
		IdleTimeout:    atoiDefault(r.FormValue("idle_timeout"), 0),
	})
}

// startUploadDeploy runs the shared redeploy pipeline for a freshly
// created upload app in the background, streaming progress into the
// app's build-log sink.
func (s *Server) startUploadDeploy(base context.Context, app *store.App, operation *store.AppOperation) {
	d, sink, progress := s.deploySinkFor(app.ID, operation)
	s.submitDeployment(func() {
		s.deploySem <- struct{}{}
		defer func() { <-s.deploySem }()
		var runErr error
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("internal deploy panic: %v", p)
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", runErr.Error())
				sink.Append("✕ " + runErr.Error())
				progress.failure(runErr)
			}
			status := store.OperationStatusCompleted
			if runErr != nil {
				status = store.OperationStatusFailed
			}
			s.finishDeploymentOperation(operation, status, runErr)
		}()
		ctx, cancel := context.WithTimeout(base, deployTimeout)
		defer cancel()
		if err := d.RedeployClaimed(ctx, app, operation); err != nil {
			runErr = err
			sink.Append("✕ deploy failed: " + err.Error())
			progress.failure(err)
			return
		}
		sink.Append("✓ deploy complete")
		progress.success()
	})
}

// startRedeploy runs the stored-source redeploy path for web and API callers.
func (s *Server) startRedeploy(base context.Context, app *store.App, operation *store.AppOperation) {
	d, sink, progress := s.deploySinkFor(app.ID, operation)
	s.submitDeployment(func() {
		s.deploySem <- struct{}{}
		defer func() { <-s.deploySem }()
		var runErr error
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("internal redeploy panic: %v", p)
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", runErr.Error())
				sink.Append("✕ " + runErr.Error())
				progress.failure(runErr)
			}
			status := store.OperationStatusCompleted
			if runErr != nil {
				status = store.OperationStatusFailed
			}
			s.finishDeploymentOperation(operation, status, runErr)
		}()
		ctx, cancel := context.WithTimeout(base, deployTimeout)
		defer cancel()
		if err := d.RedeployClaimed(ctx, app, operation); err != nil {
			runErr = err
			sink.Append("✕ redeploy failed: " + err.Error())
			progress.failure(err)
			return
		}
		sink.Append("✓ redeploy complete")
		progress.success()
	})
}

// deploySinkFor returns a per-deploy copy of the deployer whose Log,
// Stage and runtime hooks stream into the app's compatibility build-log
// cache and the durable operation feed. The shared deployer/runtime are
// never mutated, so concurrent deploys don't interleave their feeds.
func (s *Server) deploySinkFor(appID string, operation *store.AppOperation) (*deployer.Deployer, *buildLog, *deployProgress) {
	app, _ := s.Store.GetApp(context.Background(), appID)
	if app == nil && operation != nil {
		app, _ = s.Store.GetApp(context.Background(), operation.AppID)
	}
	if app == nil {
		app = &store.App{ID: appID, Name: appID, Slug: appID, Env: map[string]string{}}
	}
	sink := s.BuildLogs.get(app.Slug)
	sink.Reset()
	progress := newDeployProgress(s.Store, app, operation)
	d := *s.Deployer
	rt := *s.Deployer.Runtime
	rt.Log = func(text string) {
		sink.Append(text)
		progress.log(text)
	}
	d.Runtime = &rt
	d.Opts.Log = func(format string, args ...any) {
		text := fmt.Sprintf(format, args...)
		sink.Append(text)
		progress.log(text)
	}
	d.Opts.Stage = func(stage string) {
		sink.Stage(stage)
		progress.stage(stage)
	}
	return &d, sink, progress
}

// deployAsync runs the shared deploy pipeline with bounded concurrency,
// streaming progress and image-build output into the app's build log;
// every failure is recorded on the app row by the deployer.
func (s *Server) deployAsync(base context.Context, req deployer.GitRequest, operation *store.AppOperation) {
	s.deploySem <- struct{}{}
	defer func() { <-s.deploySem }()

	appID := ""
	if operation != nil {
		appID = operation.AppID
	}
	d, sink, progress := s.deploySinkFor(appID, operation)

	var runErr error
	defer func() {
		if p := recover(); p != nil {
			runErr = fmt.Errorf("internal deploy panic: %v", p)
			if app, dbErr := s.Store.GetApp(context.Background(), appID); dbErr == nil {
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", runErr.Error())
			}
			sink.Append("✕ " + runErr.Error())
			progress.failure(runErr)
		}
		status := store.OperationStatusCompleted
		if runErr != nil {
			status = store.OperationStatusFailed
		}
		s.finishDeploymentOperation(operation, status, runErr)
	}()

	ctx, cancel := context.WithTimeout(base, deployTimeout)
	defer cancel()
	if _, err := d.DeployGitClaimed(ctx, req, operation); err != nil {
		runErr = err
		sink.Append("✕ deploy failed: " + err.Error())
		progress.failure(err)
		return
	}
	sink.Append("✓ deploy complete")
	progress.success()
}

type deployStatusData struct {
	Status    string         `json:"status"`
	LastError string         `json:"last_error,omitempty"`
	Seq       int            `json:"seq"`
	Lines     []buildLogLine `json:"lines,omitempty"`
}

// handleDeployStatus serves the deploy progress feed polled by the
// app detail page: the row's status plus any build-log lines after
// the client's last seen seq (?after=seq).
func (s *Server) handleDeployStatus(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	lines, seq := s.BuildLogs.Since(a.Slug, after)
	for i := range lines {
		lines[i].Text = boundedDeploymentLog(lines[i].Text)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(deployStatusData{
		Status:    a.Status,
		LastError: boundedDeploymentError(a.LastError),
		Seq:       seq,
		Lines:     lines,
	})
}

func nameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := filepath.Base(u.Path)
	for _, ext := range []string{".git", ".git/"} {
		if strings.HasSuffix(p, ext) {
			p = strings.TrimSuffix(p, ext)
		}
	}
	return p
}

// handleDrop is the zero-config dropzone endpoint: upload an archive and
// deploy it as a web app (static assets or a Dockerfile). It shares the
// wizard's creation path and returns JSON so the fetch()-driven dropzone
// can show progress and redirect.
func (s *Server) handleDrop(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		jsonError(w, http.StatusBadRequest, "could not parse upload (is it larger than the size limit?): "+err.Error())
		return
	}
	app, err := s.createUploadFromRequest(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	operation, opErr := s.Store.ClaimAppOperation(r.Context(), app.ID, audit.ActionAppDeploy)
	if opErr != nil {
		_ = s.deleteApp(r.Context(), app)
		jsonError(w, http.StatusInternalServerError, opErr.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppUpload, "app", app.ID, app.Name, audit.OutcomeSuccess, "archive "+app.SourceRef)
	s.startUploadDeploy(s.backgroundAuditCtx(r), app, operation)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q,"operation_id":%q,"status_url":%q}`, app.Name, operation.ID, "/deployments/"+operation.ID)
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
