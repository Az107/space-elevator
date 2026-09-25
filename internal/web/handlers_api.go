package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

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
	aCopy := *a
	aCopy.LastError = boundedDeploymentError(aCopy.LastError)
	a = &aCopy
	view := &apiAppView{
		App:           a,
		Domains:       []string{},
		SecretKeys:    []string{},
		RuntimeStatus: a.Status,
		Services:      []string{},
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
			sort.Strings(view.Services)
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
		if errors.Is(err, store.ErrNotFound) {
			jsonError(w, http.StatusNotFound, "app not found")
		} else {
			jsonError(w, http.StatusInternalServerError, "could not read app")
		}
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
	// Kind selects the workload class: "web" (default), "function", or
	// "custom". Function fields are read when Kind == "function".
	Kind           string `json:"kind"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	Entrypoint     string `json:"entrypoint"`
	ScaleToZero    bool   `json:"scale_to_zero"`
	IdleTimeout    int    `json:"idle_timeout"`
}

type apiBuildBody struct {
	Mode         string `json:"mode"`
	Image        string `json:"image"`
	BuildCommand string `json:"build_command"`
	RunCommand   string `json:"run_command"`
	ServePath    string `json:"serve_path"`
	Port         int    `json:"port"`
}

func (s *Server) apiCreateApp(w http.ResponseWriter, r *http.Request) {
	var in apiCreateAppBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	rawURL := strings.TrimSpace(in.URL)
	if rawURL == "" {
		jsonError(w, http.StatusBadRequest, "url required")
		return
	}
	in.URL = builder.NormalizeGitURL(rawURL)
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
	if webRef, ok := builder.RefFromWebURL(rawURL); ok && in.Ref == "main" {
		in.Ref = webRef
	}
	for _, k := range keysOf(in.Env, in.Secrets) {
		if !store.ValidEnvKey(k) {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid env/secret key %q: must match [A-Za-z_][A-Za-z0-9_]*", k))
			return
		}
	}
	kind := in.Kind
	if kind == "" {
		kind = store.KindWeb
	}
	switch kind {
	case store.KindWeb, store.KindFunction, store.KindCustom:
	default:
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("unknown kind %q (use web, function, or custom)", kind))
		return
	}
	fb := builder.FunctionBuild{
		Language:   in.Runtime,
		Version:    in.RuntimeVersion,
		Entrypoint: in.Entrypoint,
	}
	if kind == store.KindFunction {
		if err := fb.Validate(); err != nil {
			jsonError(w, http.StatusBadRequest, "function: "+err.Error())
			return
		}
	}
	var buildReq *builder.CustomBuild
	var staticReq *builder.StaticBuild
	if in.Build != nil && kind != store.KindFunction {
		mode := in.Build.Mode
		if mode == "" {
			mode = store.BuildModeCustom
		}
		switch mode {
		case store.BuildModeStatic:
			image := in.Build.Image
			if image == "" {
				image = "node:20-bookworm"
			}
			port := in.Build.Port
			if port == 0 {
				port = builder.DefaultStaticListenPort
			}
			staticReq = &builder.StaticBuild{BuilderImage: image, BuildCommand: in.Build.BuildCommand, ServePath: in.Build.ServePath, ListenPort: port}
			if err := staticReq.Validate(); err != nil {
				jsonError(w, http.StatusBadRequest, "static build: "+err.Error())
				return
			}
		case store.BuildModeCustom, "server":
			buildReq = &builder.CustomBuild{BuilderImage: in.Build.Image, BuildCommand: in.Build.BuildCommand, RunCommand: in.Build.RunCommand, ListenPort: in.Build.Port}
			if err := buildReq.Validate(); err != nil {
				jsonError(w, http.StatusBadRequest, "build: "+err.Error())
				return
			}
		default:
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("unknown build mode %q (use custom, server, or static)", mode))
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
		ID:             uuid.NewString(),
		Name:           in.Name,
		SourceType:     "git",
		SourceRef:      in.URL,
		GitRef:         in.Ref,
		Env:            in.Env,
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		ScaleToZero:    in.ScaleToZero,
		IdleTimeout:    in.IdleTimeout,
		Status:         "pending",
	}
	if buildReq != nil {
		app.BuildMode = store.BuildModeCustom
		app.BuilderImage = buildReq.BuilderImage
		app.BuildCommand = buildReq.BuildCommand
		app.RunCommand = buildReq.RunCommand
		app.ListenPort = buildReq.ListenPort
	} else if staticReq != nil {
		app.BuildMode = store.BuildModeStatic
		app.BuilderImage = staticReq.BuilderImage
		app.BuildCommand = staticReq.BuildCommand
		app.ServePath = staticReq.ServePath
		app.ListenPort = staticReq.ListenPort
	}
	if kind == store.KindFunction {
		app.ListenPort = builder.DefaultFunctionPort
	}
	if err := s.Store.CreateApp(r.Context(), app); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	operation, err := s.Store.ClaimAppOperation(r.Context(), app.ID, audit.ActionAppDeploy)
	if err != nil {
		_ = s.deleteApp(r.Context(), app)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range in.Secrets {
		if err := s.Store.SetSecret(r.Context(), app.ID, k, v); err != nil {
			_ = s.deleteApp(r.Context(), app)
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.recordAudit(r, audit.ActionAppCreate, "app", app.ID, app.Name, audit.OutcomeSuccess, "git "+in.URL+" ref "+in.Ref)

	s.submitDeployment(func() {
		s.deployAsync(s.backgroundAuditCtx(r), deployer.GitRequest{
			Name:           in.Name,
			RepoURL:        in.URL,
			Ref:            in.Ref,
			Env:            in.Env,
			Secrets:        in.Secrets,
			Build:          buildReq,
			StaticBuild:    staticReq,
			Kind:           kind,
			Runtime:        fb.Language,
			RuntimeVersion: fb.Version,
			Entrypoint:     fb.Entrypoint,
			ScaleToZero:    in.ScaleToZero,
			IdleTimeout:    in.IdleTimeout,
		}, operation)
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":         in.Name,
		"status":       "pending",
		"operation_id": operation.ID,
		"status_url":   "/api/v1/deployments/" + operation.ID,
	})
}

// apiUploadApp deploys an uploaded archive (multipart/form-data). It
// shares the wizard/dropzone creation path; the response is a 202 and
// the deploy runs asynchronously.
//
// Form fields: tarball (required), kind (web|function|custom), name,
// language, runtime_version, entrypoint, env, secrets, scale_to_zero,
// idle_timeout.
func (s *Server) apiUploadApp(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "could not parse multipart upload: "+err.Error())
		return
	}
	app, err := s.createUploadFromRequest(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := s.Store.ClaimAppOperation(r.Context(), app.ID, audit.ActionAppDeploy)
	if err != nil {
		_ = s.deleteApp(r.Context(), app)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppUpload, "app", app.ID, app.Name, audit.OutcomeSuccess, "archive "+app.SourceRef)
	s.startUploadDeploy(s.backgroundAuditCtx(r), app, operation)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":         app.Name,
		"status":       "pending",
		"operation_id": operation.ID,
		"status_url":   "/api/v1/deployments/" + operation.ID,
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
		// Drops may have been replaced by an update release. Validate the
		// current release source first, then fall back to the original drop.
		sourceDir, err := s.appRuntimeSourceDir(r.Context(), a)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "stored release source is invalid: "+err.Error())
			return
		}
		if info, statErr := os.Lstat(sourceDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			jsonError(w, http.StatusBadRequest, "source directory missing; deploy again from the original source")
			return
		}
	}
	operation, claimErr := s.Store.ClaimAppOperation(r.Context(), a.ID, audit.ActionAppRedeploy)
	if claimErr != nil {
		if errors.Is(claimErr, store.ErrConflict) {
			jsonError(w, http.StatusConflict, "another update or deploy is already in progress")
		} else {
			jsonError(w, http.StatusInternalServerError, claimErr.Error())
		}
		return
	}
	_ = s.Store.UpdateAppStatusErr(r.Context(), a.ID, "pending", "")
	s.recordAudit(r, audit.ActionAppRedeploy, "app", a.ID, a.Name, audit.OutcomeSuccess, "requested")
	base := s.backgroundAuditCtx(r)
	s.startRedeploy(base, a, operation)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":         a.Name,
		"status":       "redeploying",
		"operation_id": operation.ID,
		"status_url":   "/api/v1/deployments/" + operation.ID,
	})
}

// apiPutAppEnv replaces the app's plain environment from a flat JSON
// object ({}, omitting keys, deletes them). Applied on next redeploy.
func (s *Server) apiPutAppEnv(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
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
	s.recordAudit(r, audit.ActionEnvUpdate, "app", a.ID, a.Name, audit.OutcomeSuccess, fmt.Sprintf("%d keys", len(body)))
	updated, err := s.Store.GetAppByName(r.Context(), a.Name)
	if err != nil || updated == nil {
		jsonError(w, http.StatusInternalServerError, "could not read updated app")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": updated.Env, "note": "applied on next redeploy"})
}

// apiPutAppSecrets upserts secrets from a flat JSON object. Values are
// accepted but never returned anywhere.
func (s *Server) apiPutAppSecrets(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
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
	s.recordAudit(r, audit.ActionSecretSet, "app", a.ID, a.Name, audit.OutcomeSuccess, fmt.Sprintf("%d keys", len(body)))
	keys, _ := s.Store.ListSecretKeys(r.Context(), a.ID)
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "note": "applied on next redeploy"})
}

// apiDeleteSecret removes one secret. Idempotent.
func (s *Server) apiDeleteSecret(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
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
	s.recordAudit(r, audit.ActionSecretDelete, "app", a.ID, a.Name, audit.OutcomeSuccess, "key "+key)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (s *Server) apiAddDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
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
		s.recordAudit(r, audit.ActionDomainAdd, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		apiDomainError(w, err)
		return
	}
	s.recordAudit(r, audit.ActionDomainAdd, "app", a.ID, a.Name, audit.OutcomeSuccess, "domain "+body.Domain)
	writeJSON(w, http.StatusOK, map[string]any{"domains": stringSlice(s.Store.GetAppDomains(r.Context(), a.ID))})
}

func (s *Server) apiRemoveDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
		return
	}
	if err := s.detachDomain(r.Context(), a, chi.URLParam(r, "domain")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionDomainRemove, "app", a.ID, a.Name, audit.OutcomeSuccess, "domain "+chi.URLParam(r, "domain"))
	writeJSON(w, http.StatusOK, map[string]any{"domains": stringSlice(s.Store.GetAppDomains(r.Context(), a.ID))})
}

func (s *Server) apiRestartApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
		return
	}
	if err := s.restartAppContainers(r.Context(), a); err != nil {
		s.recordAudit(r, audit.ActionAppRestart, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.syncAppRoute(r.Context(), a); err != nil {
		s.recordAudit(r, audit.ActionAppRestart, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, "publish route: "+err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppRestart, "app", a.ID, a.Name, audit.OutcomeSuccess, "")
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "restarted"})
}

func (s *Server) apiStartApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
		return
	}
	if err := s.Runtime.Start(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		s.recordAudit(r, audit.ActionAppStart, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "running")
	if err := s.syncAppRoute(r.Context(), a); err != nil {
		s.recordAudit(r, audit.ActionAppStart, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, "publish route: "+err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppStart, "app", a.ID, a.Name, audit.OutcomeSuccess, "")
	writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "status": "running"})
}

func (s *Server) apiStopApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
		return
	}
	if err := s.Runtime.Stop(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}); err != nil {
		s.recordAudit(r, audit.ActionAppStop, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Store.UpdateAppStatus(r.Context(), a.ID, "stopped")
	if err := s.syncAppRoute(r.Context(), a); err != nil {
		s.recordAudit(r, audit.ActionAppStop, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, "publish route: "+err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppStop, "app", a.ID, a.Name, audit.OutcomeSuccess, "")
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
	if !s.guardAPIIdle(w, r, a.ID) {
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
	s.recordAudit(r, audit.ActionAppRename, "app", a.ID, newName, audit.OutcomeSuccess, "was "+a.Name)
	writeJSON(w, http.StatusOK, map[string]any{"name": newName, "status": "renamed"})
}

func (s *Server) apiDeleteApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.apiAppOr404(w, r)
	if !ok {
		return
	}
	if !s.guardAPIIdle(w, r, a.ID) {
		return
	}
	if err := s.deleteApp(r.Context(), a); err != nil {
		s.recordAudit(r, audit.ActionAppRemove, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionAppRemove, "app", a.ID, a.Name, audit.OutcomeSuccess, "source "+a.SourceType)
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
