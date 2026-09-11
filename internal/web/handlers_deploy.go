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

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
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
	s.Renderer.Render(w, r, "deploy.html", deployFormData{PageData: pageCtx(r, "Deploy")})
}

func (s *Server) handleDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	// Advanced section: a repo that isn't containerized gets a
	// synthesized single-stage Dockerfile (builder image + build/run
	// commands). Any of image/run_cmd switches to custom build mode.
	builderImage := strings.TrimSpace(r.FormValue("image"))
	buildCmd := strings.TrimSpace(r.FormValue("build_cmd"))
	runCmd := strings.TrimSpace(r.FormValue("run_cmd"))
	port, portErr := builder.ParsePort(r.FormValue("port"))
	custom := builderImage != "" || runCmd != "" || buildCmd != ""
	if custom {
		cb := builder.CustomBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			RunCommand:   runCmd,
			ListenPort:   port,
		}
		if err := cb.Validate(); err != nil {
			s.Renderer.Render(w, r, "deploy.html", deployFormData{
				PageData: pageCtx(r, "Deploy"),
				Error:    "Advanced deploy: " + err.Error() + ".",
			})
			return
		}
	}
	if !custom && portErr == nil && r.FormValue("port") != "" {
		// A port with no build settings means the user half-filled the
		// advanced section; surface it instead of silently ignoring.
		portErr = errors.New("builder image or run command required when setting a port")
	}
	if portErr != nil {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "Deploy"),
			Error:    "Advanced deploy: " + portErr.Error() + ".",
		})
		return
	}

	if name == "" {
		name = strings.ToLower(nameFromURL(repoURL))
	}
	fail := func(msg string) {
		s.Renderer.Render(w, r, "deploy.html", deployFormData{
			PageData: pageCtx(r, "Deploy"),
			Error:    msg,
		})
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
		ID:           appID,
		Name:         name,
		Slug:         name,
		SourceType:   "git",
		SourceRef:    repoURL,
		GitRef:       ref,
		Env:          env,
		BuildMode:    buildMode,
		BuilderImage: builderImage,
		BuildCommand: buildCmd,
		RunCommand:   runCmd,
		ListenPort:   port,
		Status:       "pending",
	}
	if err := s.Store.CreateApp(r.Context(), app); err != nil {
		fail(fmt.Sprintf("Could not create app %q: %v.", name, err))
		return
	}

	var buildReq *builder.CustomBuild
	if custom {
		buildReq = &builder.CustomBuild{
			BuilderImage: builderImage,
			BuildCommand: buildCmd,
			RunCommand:   runCmd,
			ListenPort:   port,
		}
	}
	go s.deployAsync(deployer.GitRequest{
		Name:    name,
		RepoURL: repoURL,
		Ref:     ref,
		Env:     env,
		Secrets: secrets,
		Build:   buildReq,
	})
	http.Redirect(w, r, "/apps/"+name, http.StatusSeeOther)
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
func (s *Server) deployAsync(req deployer.GitRequest) {
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

	ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
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

// handleDrop handles a multipart tarball upload from the drag-drop UI.
func (s *Server) handleDrop(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "could not parse upload (is it larger than the size limit?): "+err.Error())
		return
	}
	f, header, err := r.FormFile("tarball")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "missing 'tarball' file field — drag a single .tar.gz / .zip file, not a folder")
		return
	}
	defer f.Close()

	if header.Size == 0 {
		jsonError(w, http.StatusBadRequest, "uploaded file is empty")
		return
	}

	id := uuid.NewString()
	tarPath := filepath.Join(s.Cfg.AppsRoot, "drops", id+".tar.gz")
	destDir := filepath.Join(s.Cfg.AppsRoot, "drops", id)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out, err := os.Create(tarPath)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Close()

	kind, err := extractAndDetect(tarPath, destDir)
	if err != nil {
		// Clean up the partial extraction so we don't leave junk on disk
		// when the upload turns out to be bad.
		_ = os.RemoveAll(destDir)
		_ = os.Remove(tarPath)
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("could not extract %q: %v", header.Filename, err))
		return
	}
	// Tarball is no longer needed — the extracted source dir is the input
	// for build and redeploy. Drop it now so it doesn't accumulate on
	// disk or sit there waiting to be exfiltrated via an arbitrary-read
	// path in the future.
	_ = os.Remove(tarPath)

	name := "drop-" + id[:8]

	composeBytes, err := syntheticCompose(destDir, kind)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kind == "static" {
		// baseHref is the URL prefix the app will be served at. If the
		// user attaches a custom domain, the app is served at the
		// subdomain root and base href should be "/". Otherwise the app
		// lives under the auto path-prefix route and base href should
		// mirror that, including the synth compose's "-web" service
		// suffix (Traefik keys the stripPrefix and router by that).
		baseHref := "/"
		if s.Cfg.AppPathPrefix != "" {
			prefix := strings.TrimRight(s.Cfg.AppPathPrefix, "/")
			baseHref = prefix + "/" + name + "-web/"
		}
		root, err := builder.WriteStaticFiles(destDir, baseHref)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if root != "" && root != "." {
			fmt.Fprintf(os.Stderr, "info: detected nested doc root %q for %s\n", root, header.Filename)
		}
	}
	app := &store.App{
		ID:          id,
		Name:        name,
		Slug:        name,
		SourceType:  "drop",
		SourceRef:   header.Filename,
		GitRef:      "",
		DropKind:    kind,
		ComposeYAML: composeBytes,
		Env:         map[string]string{},
		Status:      "pending",
	}
	if err := s.Store.CreateApp(r.Context(), app); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	spec, err := composer.Parse([]byte(composeBytes))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d, sink := s.deploySinkFor(name)
	go func() {
		s.deploySem <- struct{}{}
		defer func() { <-s.deploySem }()
		ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
		defer cancel()
		env, err := s.Store.LoadRuntimeEnv(ctx, id, app.Env)
		if err != nil {
			_ = s.Store.UpdateAppStatusErr(ctx, id, "error", "load env: "+err.Error())
			sink.Append("✕ deploy failed: load env: " + err.Error())
			return
		}
		meta := composer.AppMeta{
			ID:         id,
			Name:       app.Slug,
			Env:        env,
			Label:      app.Slug,
			BuildEnv:   app.Env,
			StaticDrop: kind == "static",
		}
		sink.Stage("deploy")
		sink.appendf("Deploying %s...", name)
		if err := d.Runtime.Deploy(ctx, meta, spec, destDir); err != nil {
			_ = s.Store.UpdateAppStatusErr(ctx, id, "error", "deploy failed: "+err.Error())
			sink.Append("✕ deploy failed: " + err.Error())
			return
		}
		_ = s.Store.UpdateAppStatusErr(ctx, id, "running", "")
		sink.Stage("route")
		sink.Append("✓ deploy complete")
	}()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q}`, name)
}
