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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type deployFormData struct {
	PageData
	Error string
}

// deployTimeout bounds a full clone+build+run deploy so a stuck git
// server or image build can't wedge a worker forever.
const deployTimeout = 30 * time.Minute

// trailing hyphens are disallowed so names can't be glued into double
// dashes in downstream keys (image tags, Traefik router names).
var appNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validAppName enforces URL/hostname/container-safe names up front, so
// nothing downstream (paths, Traefik keys, redirects) ever sees junk.
func validAppName(name string) bool {
	return appNamePattern.MatchString(name)
}

func (s *Server) handleDeployForm(w http.ResponseWriter, r *http.Request) {
	s.Renderer.Render(w, r, "deploy.html", deployFormData{PageData: pageCtx(r, "New app")})
}

// handleDeploySubmit is the unified wizard endpoint. It dispatches on
// the "source" field: git deploys synchronously create a row and kick
// off the clone pipeline; upload deploys extract an archive and do the
// same, sharing CreateUpload with the dropzone and CLI/API.
func (s *Server) handleDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "New app"),
			Error:    "Could not parse form: " + err.Error(),
		})
		return
	}
	fail := func(msg string) {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "New app"),
			Error:    msg,
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
		s.startUploadDeploy(s.backgroundAuditCtx(r), app)
		http.Redirect(w, r, "/apps/"+app.Name, http.StatusSeeOther)
		return
	}

	repoURL := r.FormValue("url")
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	ref := r.FormValue("ref")
	if ref == "" {
		ref = "main"
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

	// Advanced section: a repo that isn't containerized gets a
	// synthesized single-stage Dockerfile (builder image + build/run
	// commands). Any of image/run_cmd switches to custom build mode.
	builderImage := strings.TrimSpace(r.FormValue("image"))
	buildCmd := strings.TrimSpace(r.FormValue("build_cmd"))
	runCmd := strings.TrimSpace(r.FormValue("run_cmd"))
	port, portErr := builder.ParsePort(r.FormValue("port"))
	custom := kind != store.KindFunction && (builderImage != "" || runCmd != "" || buildCmd != "")
	if custom {
		cb := builder.CustomBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			RunCommand:   runCmd,
			ListenPort:   port,
		}
		if err := cb.Validate(); err != nil {
			fail("Advanced deploy: " + err.Error() + ".")
			return
		}
	}
	if !custom && portErr == nil && r.FormValue("port") != "" {
		// A port with no build settings means the user half-filled the
		// advanced section; surface it instead of silently ignoring.
		portErr = errors.New("builder image or run command required when setting a port")
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
	buildMode := store.BuildModeCompose
	if custom {
		buildMode = store.BuildModeCustom
	}
	app := &store.App{
		ID:             appID,
		Name:           name,
		Slug:           name,
		SourceType:     "git",
		SourceRef:      repoURL,
		GitRef:         ref,
		Env:            env,
		BuildMode:      buildMode,
		BuilderImage:   builderImage,
		BuildCommand:   buildCmd,
		RunCommand:     runCmd,
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

	var buildReq *builder.CustomBuild
	if custom {
		buildReq = &builder.CustomBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			RunCommand:   runCmd,
			ListenPort:   port,
		}
	}
	go s.deployAsync(s.backgroundAuditCtx(r), deployer.GitRequest{
		Name:           name,
		RepoURL:        repoURL,
		Ref:            ref,
		Env:            env,
		Secrets:        secrets,
		Build:          buildReq,
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		ScaleToZero:    app.ScaleToZero,
		IdleTimeout:    app.IdleTimeout,
	})
	http.Redirect(w, r, "/apps/"+name, http.StatusSeeOther)
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
func (s *Server) startUploadDeploy(base context.Context, app *store.App) {
	d, sink := s.deploySinkFor(app.Slug)
	go func() {
		s.deploySem <- struct{}{}
		defer func() { <-s.deploySem }()
		defer func() {
			if p := recover(); p != nil {
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", fmt.Sprintf("internal deploy panic: %v", p))
				sink.Append(fmt.Sprintf("✕ internal deploy panic: %v", p))
			}
		}()
		ctx, cancel := context.WithTimeout(base, deployTimeout)
		defer cancel()
		if err := d.Redeploy(ctx, app); err != nil {
			sink.Append("✕ deploy failed: " + err.Error())
			return
		}
		sink.Append("✓ deploy complete")
	}()
}

// deploySinkFor returns a per-deploy copy of the deployer whose Log,
// Stage and runtime hooks stream into the app's build-log sink, plus
// the freshly reset sink. The shared deployer/runtime are never
// mutated, so concurrent deploys don't interleave their feeds.
func (s *Server) deploySinkFor(name string) (*deployer.Deployer, *buildLog) {
	sink := s.BuildLogs.get(name)
	sink.Reset()
	d := *s.Deployer
	rt := *s.Deployer.Runtime
	rt.Log = sink.Append
	d.Runtime = &rt
	d.Opts.Log = sink.appendf
	d.Opts.Stage = sink.Stage
	return &d, sink
}

// deployAsync runs the shared deploy pipeline with bounded concurrency,
// streaming progress and image-build output into the app's build log;
// every failure is recorded on the app row by the deployer.
func (s *Server) deployAsync(base context.Context, req deployer.GitRequest) {
	s.deploySem <- struct{}{}
	defer func() { <-s.deploySem }()

	d, sink := s.deploySinkFor(req.Name)

	defer func() {
		if p := recover(); p != nil {
			if app, err := s.Store.GetAppByName(context.Background(), req.Name); err == nil {
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", fmt.Sprintf("internal deploy panic: %v", p))
			}
			sink.Append(fmt.Sprintf("✕ internal deploy panic: %v", p))
		}
	}()

	ctx, cancel := context.WithTimeout(base, deployTimeout)
	defer cancel()
	if _, err := d.DeployGit(ctx, req); err != nil {
		sink.Append("✕ deploy failed: " + err.Error())
		return
	}
	sink.Append("✓ deploy complete")
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(deployStatusData{
		Status:    a.Status,
		LastError: a.LastError,
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
	s.recordAudit(r, audit.ActionAppUpload, "app", app.ID, app.Name, audit.OutcomeSuccess, "archive "+app.SourceRef)
	s.startUploadDeploy(s.backgroundAuditCtx(r), app)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q}`, app.Name)
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
