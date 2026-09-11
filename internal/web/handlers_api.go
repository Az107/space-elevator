package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// apiRedeployTimeout bounds an async redeploy started via the API;
// matches the web handler's budget.
const apiRedeployTimeout = 30 * time.Minute

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiAppView is the JSON representation of an app plus live state.
// Secret values never appear here; secret_keys lists names only.
type apiAppView struct {
	*store.App
	Domains       []string `json:"domains"`
	SecretKeys    []string `json:"secret_keys"`
	RuntimeStatus string   `json:"runtime_status"`
	Services      []string `json:"services"`
}

func (s *Server) appView(ctx context.Context, a *store.App, live bool) *apiAppView {
	view := &apiAppView{
		App:         a,
		Domains:     []string{},
		SecretKeys:  []string{},
		RuntimeStatus: a.Status,
		Services:    []string{},
	}
	view.Domains, _ = s.Store.GetAppDomains(ctx, a.ID)
	view.SecretKeys, _ = s.Store.ListSecretKeys(ctx, a.ID)
	if live {
		if spec, err := composer.Parse([]byte(a.ComposeYAML)); err == nil {
			status, _, _ := s.Runtime.Status(ctx, composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}, spec)
			if status != "" {
				view.RuntimeStatus = status
			}
			for k := range spec.Services {
				view.Services = append(view.Services, k)
			}
		}
	}
	return view
}

func (s *Server) apiListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListApps(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]*apiAppView, 0, len(apps))
	for _, a := range apps {
		out = append(out, s.appView(r.Context(), a, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) apiGetApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.appView(r.Context(), a, true))
}

func (s *Server) apiAppOr404(w http.ResponseWriter, r *http.Request) (*store.App, bool) {
	a, err := s.Store.GetAppByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "app not found")
		return nil, false
	}
	return a, true
}

// apiCreateAppBody is the POST /api/v1/apps payload. Env and secrets
// are native JSON objects here (the KEY=VALUE line format is a web-form
// concern).
type apiCreateAppBody struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Ref     string            `json:"ref"`
	Env     map[string]string `json:"env"`
	Secrets map[string]string `json:"secrets"`
	Build   *apiBuildBody     `json:"build"`
}

type apiBuildBody struct {
	Image        string `json:"image"`
	BuildCommand string `json:"build_command"`
	RunCommand   string `json:"run_command"`
	Port         int    `json:"port"`
}

func (s *Server) apiCreateApp(w http.ResponseWriter, r *http.Request) {
	var in apiCreateAppBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if in.URL == "" {
		jsonError(w, http.StatusBadRequest, "url required")
		return
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if in.Name == "" {
		in.Name = strings.ToLower(strings.TrimSpace(nameFromURL(in.URL)))
	}
	if in.Name == "" {
		jsonError(w, http.StatusBadRequest, "name required (could not derive it from the URL)")
		return
	}
	if !deployer.ValidAppName(in.Name) {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", in.Name))
		return
	}
	if in.Ref == "" {
		in.Ref = "main"
	}
	for _, k := range keysOf(in.Env, in.Secrets) {
		if !store.ValidEnvKey(k) {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid env/secret key %q: must match [A-Za-z_][A-Za-z0-9_]*", k))
			return
		}
	}
	var buildReq *builder.CustomBuild
	if in.Build != nil {
		buildReq = &builder.CustomBuild{
			BuilderImage: in.Build.Image,
			BuildCommand: in.Build.BuildCommand,
			RunCommand:   in.Build.RunCommand,
			ListenPort:   in.Build.Port,
		}
		if err := buildReq.Validate(); err != nil {
			jsonError(w, http.StatusBadRequest, "build: "+err.Error())
			return
		}
	}
	if _, err := s.Store.GetAppByName(r.Context(), in.Name); err == nil {
		jsonError(w, http.StatusConflict, fmt.Sprintf("app %q already exists", in.Name))
		return
	}

	// Same up-front row creation as the web flow: the 202 points at a
	// resource that already exists, and failures land on the row.
	app := &store.App{
		ID:         uuid.NewString(),
		Name:       in.Name,
		SourceType: "git",
		SourceRef:  in.URL,
		GitRef:     in.Ref,
		Env:        in.Env,
		Status:     "pending",
	}
	if buildReq != nil {
		app.BuildMode = store.BuildModeCustom
		app.BuilderImage = buildReq.BuilderImage
		app.BuildCommand = buildReq.BuildCommand
		app.RunCommand = buildReq.RunCommand
		app.ListenPort = buildReq.ListenPort
	}
	if err := s.Store.CreateApp(r.Context(), app); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	go s.deployAsync(deployer.GitRequest{
		Name:    in.Name,
		RepoURL: in.URL,
		Ref:     in.Ref,
		Env:     in.Env,
		Secrets: in.Secrets,
		Build:   buildReq,
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":   in.Name,
		"status": "pending",
	})
}

// apiRedeployApp tears down and re-deploys from the stored source.
// Asynchronous like the web UI; poll GET /apps/{name} for the outcome.
func (s *Server) apiRedeployApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if a.SourceType != "git" {
		// Git apps don't need the checkout to exist — Redeploy falls
		// back to the full clone pipeline. Drops do.
		sourceDir, err := store.AppSourceDir(s.Cfg.AppsRoot, a)
		if err == nil {
			if _, statErr := os.Stat(sourceDir); statErr != nil {
				jsonError(w, http.StatusBadRequest, "source directory missing; deploy again from the original source")
				return
			}
		}
	}
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "pending", "")
	go func(app *store.App) {
		defer func() {
			if p := recover(); p != nil {
				_ = s.Store.UpdateAppStatusErr(context.Background(), app.ID, "error", fmt.Sprintf("internal redeploy panic: %v", p))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), apiRedeployTimeout)
		defer cancel()
		_ = s.Deployer.Redeploy(ctx, app)
	}(a)
	writeJSON(w, http.StatusAccepted, map[string]any{"name": a.Name, "status": "redeploying"})
}

// apiPutAppEnv replaces the app's plain environment from a flat JSON
// object ({}, omitting keys, deletes them). Applied on next redeploy.
func (s *Server) apiPutAppEnv(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	var body map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	for k := range body {
		if !store.ValidEnvKey(k) {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid key %q: must match [A-Za-z_][A-Za-z0-9_]*", k))
			return
		}
	}
	if err := s.Store.UpdateAppEnv(r.Context(), a.ID, body); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, _ := s.Store.GetAppByName(r.Context(), a.Name)
	writeJSON(w, http.StatusOK, map[string]any{"env": updated.Env, "note": "applied on next redeploy"})
}

// apiPutAppSecrets upserts secrets from a flat JSON object. Values are
// accepted but never returned anywhere.
func (s *Server) apiPutAppSecrets(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	var body map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	for k := range body {
		if !store.ValidEnvKey(k) {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid key %q: must match [A-Za-z_][A-Za-z0-9_]*", k))
			return
		}
	}
	for k, v := range body {
		if err := s.Store.SetSecret(r.Context(), a.ID, k, v); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	keys, _ := s.Store.ListSecretKeys(r.Context(), a.ID)
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "note": "applied on next redeploy"})
}

// apiDeleteSecret removes one secret. Idempotent.
func (s *Server) apiDeleteSecret(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if !store.ValidEnvKey(key) {
		jsonError(w, http.StatusBadRequest, "invalid secret key")
		return
	}
	if err := s.Store.DeleteSecret(r.Context(), a.ID, key); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (s *Server) apiAddDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.attachDomain(r.Context(), a, body.Domain); err != nil {
		apiDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": stringSlice(s.Store.GetAppDomains(r.Context(), a.ID))})
}

func (s *Server) apiRemoveDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if err := s.detachDomain(r.Context(), a, chi.URLParam(r, "domain")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": stringSlice(s.Store.GetAppDomains(r.Context(), a.ID))})
}

func (s *Server) apiRestartApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if err := s.restartAppContainers(r.Context(), a); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "restarted"})
}

func (s *Server) apiStartApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if err := s.Runtime.Start(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "running")
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "running"})
}

func (s *Server) apiStopApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if err := s.Runtime.Stop(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "stopped")
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "stopped"})
}

// apiRenameApp changes the display name only. Containers, routes, images
// and domains keep using the app's stable runtime slug, so nothing needs
// to be rebuilt.
func (s *Server) apiRenameApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	newName := strings.ToLower(strings.TrimSpace(body.Name))
	if !deployer.ValidAppName(newName) {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", newName))
		return
	}
	if newName == a.Name {
		writeJSON(w, http.StatusOK, map[string]any{"name": newName, "status": "unchanged"})
		return
	}
	if existing, err := s.Store.GetAppByName(r.Context(), newName); err == nil && existing != nil {
		jsonError(w, http.StatusConflict, fmt.Sprintf("app %q already exists", newName))
		return
	}
	if err := s.Store.UpdateAppName(r.Context(), a.ID, newName); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": newName, "status": "renamed"})
}

func (s *Server) apiDeleteApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if err := s.deleteApp(r.Context(), a); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "removed"})
}

func keysOf(maps ...map[string]string) []string {
	var keys []string
	for _, m := range maps {
		for k := range m {
			keys = append(keys, k)
		}
	}
	return keys
}

// stringSlice normalizes an (domains, err) pair into a non-nil slice
// so JSON responses encode [] instead of null.
func stringSlice(v []string, err error) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// apiDomainError maps attachDomain failures to statuses.
func apiDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidDomain), errors.Is(err, errDomainReserved):
		jsonError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errDomainTaken):
		jsonError(w, http.StatusConflict, err.Error())
	default:
		jsonError(w, http.StatusInternalServerError, err.Error())
	}
}
