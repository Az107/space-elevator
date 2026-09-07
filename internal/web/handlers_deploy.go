package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type deployFormData struct {
	PageData
	Error string
}

func (s *Server) handleDeployForm(w http.ResponseWriter, r *http.Request) {
	s.Renderer.Render(w, "deploy.html", deployFormData{PageData: PageData{Authed: true}})
}

func (s *Server) handleDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	repoURL := r.FormValue("url")
	name := r.FormValue("name")
	ref := r.FormValue("ref")
	if ref == "" {
		ref = "main"
	}
	if name == "" {
		name = nameFromURL(repoURL)
	}
	if repoURL == "" || name == "" {
		s.Renderer.Render(w, "deploy.html", deployFormData{
			PageData: PageData{Authed: true},
			Error:    "URL and name required."})
		return
	}

	if _, err := s.Store.GetAppByName(r.Context(), name); err == nil {
		s.Renderer.Render(w, "deploy.html", deployFormData{
			PageData: PageData{Authed: true},
			Error:    fmt.Sprintf("app %q already exists; remove it first or use a different name.", name)})
		return
	}

	go s.deployAsync(name, repoURL, ref)
	http.Redirect(w, r, "/apps/"+name, http.StatusSeeOther)
}

func (s *Server) deployAsync(name, repoURL, ref string) {
	ctx := context.Background()
	host, _ := url.Parse(repoURL)
	auth := &builder.Auth{}
	if cred, err := s.Store.GetCredentialForHost(ctx, host.Host); err == nil {
		auth.Username = cred.Username
		auth.Token = cred.Token
	}

	appID := uuid.NewString()
	sourceDir := filepath.Join(s.Cfg.AppsRoot, "sources", appID)
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		return
	}
	if err := builder.Clone(ctx, repoURL, ref, sourceDir, auth); err != nil {
		return
	}
	composePath, err := builder.FindComposeFile(sourceDir)
	if err != nil {
		return
	}
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		return
	}
	spec, err := composer.Parse(composeBytes)
	if err != nil {
		return
	}
	app := &store.App{
		ID:          appID,
		Name:        name,
		SourceType:  "git",
		SourceRef:   repoURL,
		GitRef:      ref,
		ComposeYAML: string(composeBytes),
		Env:         map[string]string{},
		Status:      "pending",
	}
	if err := s.Store.CreateApp(ctx, app); err != nil {
		return
	}
	meta := composer.AppMeta{
		ID:    app.ID,
		Name:  app.Name,
		Label: app.Name,
	}
	if err := s.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
		_ = s.Store.UpdateAppStatus(ctx, app.ID, "error")
		return
	}
	_ = s.Store.UpdateAppStatus(ctx, app.ID, "running")
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
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "could not parse upload: "+err.Error())
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
		root, err := WriteStaticFiles(destDir, baseHref)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if root != "" && root != "." {
			fmt.Fprintf(os.Stderr, "info: detected nested doc root %q for %s\n", root, header.Filename)
		}
	}
	meta := composer.AppMeta{
		ID:         id,
		Name:       name,
		Label:      name,
		StaticDrop: kind == "static",
	}

	app := &store.App{
		ID:          id,
		Name:        name,
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
	go func() {
		ctx := context.Background()
		if err := s.Runtime.Deploy(ctx, meta, spec, destDir); err != nil {
			_ = s.Store.UpdateAppStatus(ctx, id, "error")
			return
		}
		_ = s.Store.UpdateAppStatus(ctx, id, "running")
	}()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q}`, name)
}