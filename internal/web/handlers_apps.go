package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

type appsListData struct {
	PageData
	Apps          []*store.App
	DomainsByApp  map[string][]string
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListApps(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	domains := map[string][]string{}
	for _, a := range apps {
		d, _ := s.Store.GetAppDomains(r.Context(), a.ID)
		if len(d) > 0 {
			domains[a.ID] = d
		}
	}
	s.Renderer.Render(w, r, "apps.html", appsListData{
		PageData:     pageCtx(r, "Apps"),
		Apps:         apps,
		DomainsByApp: domains,
	})
}

type appDetailData struct {
	PageData
	App        *store.App
	Status     string
	Services   []string
	Domains    []string
	EnvText    string
	SecretKeys []string
	// Build panel: last lines of the deploy's build log, the latest
	// sequence number (for incremental polling), and whether a deploy
	// is currently in flight.
	BuildLines []buildLogLine
	BuildSeq   int
	Deploying  bool
}

// formatEnv renders an env map as sorted KEY=VALUE lines for the
// editor textarea.
func formatEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+env[k])
	}
	return strings.Join(lines, "\n")
}

func (s *Server) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		// A deploy in progress (or one that failed before compose
		// persistence) leaves ComposeYAML empty or invalid. Render the
		// pending/error state from the row instead of a 500.
		spec = &composer.Spec{}
	}
	// With no parseable compose there is nothing to reconcile containers
	// against — the row's own status (pending/error) is the truth.
	status := a.Status
	if len(spec.Services) > 0 {
		status, _, _ = s.Runtime.Status(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}, spec)
	}
	services := make([]string, 0, len(spec.Services))
	for k := range spec.Services {
		services = append(services, k)
	}
	domains, _ := s.Store.GetAppDomains(r.Context(), a.ID)
	secretKeys, _ := s.Store.ListSecretKeys(r.Context(), a.ID)

	buildLines, buildSeq := s.BuildLogs.Since(a.Slug, 0)
	if len(buildLines) > 100 {
		buildLines = buildLines[len(buildLines)-100:]
	}

	s.Renderer.Render(w, r, "app_detail.html", appDetailData{
		PageData:   pageCtx(r, a.Name),
		App:        a,
		Status:     status,
		Services:   services,
		Domains:    domains,
		EnvText:    formatEnv(a.Env),
		SecretKeys: secretKeys,
		BuildLines: buildLines,
		BuildSeq:   buildSeq,
		Deploying:  a.Status == "pending",
	})
}

// restartAppContainers stops and starts every container of an app.
// Env is baked into containers at create time, so restart does NOT
// pick up env/secret edits — that needs a redeploy.
func (s *Server) restartAppContainers(ctx context.Context, a *store.App) error {
	return s.Runtime.Restart(ctx, composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug})
}

func (s *Server) handleAppRestart(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.GetAppByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return
	}
	if err := s.restartAppContainers(r.Context(), a); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps/"+a.Name, "App restarted.")
}

func (s *Server) handleAppStart(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := s.Runtime.Start(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "running")
	s.redirectOK(w, r, "/apps/"+a.Name, "App started.")
}

func (s *Server) handleAppStop(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := s.Runtime.Stop(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "stopped")
	s.redirectOK(w, r, "/apps/"+a.Name, "App stopped.")
}

// handleAppRename changes only the display name and dashboard URL. The
// runtime slug is untouched, so containers, routes, images, and domains
// keep working without a redeploy.
func (s *Server) handleAppRename(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	newName := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	if !validAppName(newName) {
		s.redirectErr(w, r, "/apps/"+a.Name, fmt.Sprintf("Invalid app name %q: use 1-63 lowercase letters, digits, or hyphens.", newName))
		return
	}
	if newName == a.Name {
		s.redirectOK(w, r, "/apps/"+a.Name, "")
		return
	}
	if existing, err := s.Store.GetAppByName(r.Context(), newName); err == nil && existing != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, fmt.Sprintf("App %q already exists.", newName))
		return
	}
	if err := s.Store.UpdateAppName(r.Context(), a.ID, newName); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps/"+newName, fmt.Sprintf("Renamed to %s.", newName))
}

// deleteApp tears down runtime + route + artifacts and deletes the
// DB row. Shared by the web form and the API.
func (s *Server) deleteApp(ctx context.Context, a *store.App) error {
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		// A failed deploy may have empty or invalid stored compose.
		// Removal must still work: teardown filters by label, so an
		// empty spec only skips the image-cleanup step.
		spec = &composer.Spec{}
	}
	_ = s.Runtime.Remove(ctx, composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}, spec)
	_ = s.TraefikW.Remove(a.Slug)
	// Drop the local image and on-disk artifacts. Failures are
	// non-fatal — the app is gone from the user's perspective once
	// the DB row is deleted.
	for svcName := range spec.Services {
		tag := s.Runtime.ImageTag(a.Slug, svcName)
		_ = s.Cli.RemoveImage(ctx, tag, false)
	}
	_ = store.DeleteAppArtifacts(s.Cfg.AppsRoot, a)
	s.BuildLogs.Remove(a.Slug)
	return s.Store.DeleteApp(ctx, a.ID)
}

func (s *Server) handleAppRemove(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.GetAppByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return
	}
	if err := s.deleteApp(r.Context(), a); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps", fmt.Sprintf("App %s removed.", a.Name))
}
func (s *Server) handleAppRedeploy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return
	}
	if a.SourceType != "git" {
		// Git apps don't need the checkout to exist — Redeploy falls
		// back to the full clone pipeline. Drops do.
		sourceDir, err := store.AppSourceDir(s.Cfg.AppsRoot, a)
		if err != nil {
			s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
			return
		}
		if _, err := os.Stat(sourceDir); err != nil {
			s.redirectErr(w, r, "/apps/"+a.Name, "source directory missing; cannot redeploy")
			return
		}
	}
	// Flip the row to pending synchronously so the page the user is
	// redirected to always starts the build progress view; Redeploy
	// re-asserts it inside the pipeline.
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "pending", "")
	// The whole pipeline (synth regen, teardown, deploy, route rewrite,
	// status updates) is shared with the CLI and API via the deployer.
	// A sink-wired copy streams progress into the build panel.
	go func(app *store.App) {
		defer func() {
			if p := recover(); p != nil {
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", fmt.Sprintf("internal redeploy panic: %v", p))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		d, sink := s.deploySinkFor(app.Slug)
		if err := d.Redeploy(ctx, app); err != nil {
			sink.Append("✕ redeploy failed: " + err.Error())
			return
		}
		sink.Append("✓ redeploy complete")
	}(a)
	http.Redirect(w, r, "/apps/"+a.Name, http.StatusSeeOther)
}

// handleAppEnvSave replaces the app's plain environment from the
// KEY=VALUE textarea. Variables are baked into containers at create
// time, so edits apply on the next redeploy — the UI says so.
func (s *Server) handleAppEnvSave(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	env, bad := store.ParseKVLines(r.FormValue("env"))
	if len(bad) > 0 {
		s.redirectErr(w, r, "/apps/"+a.Name, fmt.Sprintf("Invalid line(s) %q: use KEY=VALUE per line; keys must match [A-Za-z_][A-Za-z0-9_]*.", bad))
		return
	}
	if err := s.Store.UpdateAppEnv(r.Context(), a.ID, env); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps/"+a.Name, "Environment saved; applies on next redeploy.")
}

// handleAppSecretSet upserts one secret. The value is accepted but
// never rendered back anywhere.
func (s *Server) handleAppSecretSet(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := r.FormValue("value")
	if !store.ValidEnvKey(key) {
		s.redirectErr(w, r, "/apps/"+a.Name, "Invalid key: must match [A-Za-z_][A-Za-z0-9_]*.")
		return
	}
	if err := s.Store.SetSecret(r.Context(), a.ID, key, value); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps/"+a.Name, fmt.Sprintf("Secret %s set.", key))
}

func (s *Server) handleAppSecretDelete(w http.ResponseWriter, r *http.Request) {
	a, err := s.appOr404(w, r)
	if err != nil {
		return
	}
	key := chi.URLParam(r, "key")
	if !store.ValidEnvKey(key) {
		s.redirectErr(w, r, "/apps/"+a.Name, "invalid secret key")
		return
	}
	if err := s.Store.DeleteSecret(r.Context(), a.ID, key); err != nil {
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.redirectOK(w, r, "/apps/"+a.Name, fmt.Sprintf("Secret %s deleted.", key))
}

// appOr404 resolves the {name} URL param to an app, flashing the error
// itself when the app doesn't exist.
func (s *Server) appOr404(w http.ResponseWriter, r *http.Request) (*store.App, error) {
	a, err := s.Store.GetAppByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return nil, err
	}
	return a, nil
}
