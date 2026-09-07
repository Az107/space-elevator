package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
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
	s.Renderer.Render(w, "apps.html", appsListData{
		PageData:     PageData{Authed: true},
		Apps:         apps,
		DomainsByApp: domains,
	})
}

type appDetailData struct {
	PageData
	App      *store.App
	Status   string
	Services []string
	Domains  []string
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, _, _ := s.Runtime.Status(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Name, Label: a.Name}, spec)
	services := make([]string, 0, len(spec.Services))
	for k := range spec.Services {
		services = append(services, k)
	}
	domains, _ := s.Store.GetAppDomains(r.Context(), a.ID)

	s.Renderer.Render(w, "app_detail.html", appDetailData{
		PageData: PageData{Authed: true},
		App:      a,
		Status:   status,
		Services: services,
		Domains:  domains,
	})
}

func (s *Server) handleAppRestart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	cs, err := s.Cli.ListContainersFiltered(r.Context(), true, map[string][]string{
		"label": {composer.LabelApp + "=" + a.Name},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, c := range cs {
		full, err := s.Cli.LookupID(r.Context(), c.ID)
		if err != nil || full == "" {
			continue
		}
		_ = s.Cli.StopContainer(r.Context(), full, 10)
		if err := s.Cli.StartContainer(r.Context(), full); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/%s", a.Name), http.StatusSeeOther)
}

func (s *Server) handleAppRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.Runtime.Remove(r.Context(), composer.AppMeta{ID: a.ID, Name: a.Name, Label: a.Name}, spec)
	_ = s.TraefikW.Remove(a.Name)
	// Drop the local image and on-disk artifacts. Failures are
	// non-fatal — the app is gone from the user's perspective once
	// the DB row is deleted.
	for svcName := range spec.Services {
		tag := s.Runtime.ImageTag(a.Name, svcName)
		_ = s.Cli.RemoveImage(r.Context(), tag, false)
	}
	_ = store.DeleteAppArtifacts(s.Cfg.AppsRoot, a)
	_ = s.Store.DeleteApp(r.Context(), a.ID)
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}
func (s *Server) handleAppRedeploy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	sourceDir, err := store.AppSourceDir(s.Cfg.AppsRoot, a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(sourceDir); err != nil {
		http.Error(w, "source directory missing; cannot redeploy", http.StatusBadRequest)
		return
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// For drops, regenerate Dockerfile + nginx.conf so a re-deploy picks
	// up the latest synth logic (doc-root detection, <base href>, build
	// perms). Subdomain-served apps get base href "/"; path-prefix apps
	// get the matching "/app/<name>-web/".
	if a.SourceType == "drop" && a.DropKind == "static" {
		domains, _ := s.Store.GetAppDomains(r.Context(), a.ID)
		baseHref := "/"
		if len(domains) == 0 && s.Cfg.AppPathPrefix != "" {
			baseHref = strings.TrimRight(s.Cfg.AppPathPrefix, "/") + "/" + a.Name + "-web/"
		}
		if _, err := WriteStaticFiles(sourceDir, baseHref); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	meta := composer.AppMeta{
		ID:         a.ID,
		Name:       a.Name,
		Label:      a.Name,
		StaticDrop: a.SourceType == "drop" && a.DropKind == "static",
	}
	if err := s.Runtime.Remove(r.Context(), meta, spec); err != nil {
		fmt.Fprintf(os.Stderr, "warn: redeploy remove: %v\n", err)
	}
	go func(appID, appName string) {
		ctx := context.Background()
		if err := s.Runtime.Deploy(ctx, meta, spec, sourceDir); err != nil {
			_ = s.Store.UpdateAppStatus(ctx, appID, "error")
			return
		}
		_ = s.Store.UpdateAppStatus(ctx, appID, "running")
		// rewrite the route from fresh container state
		domains, _ := s.Store.GetAppDomains(ctx, appID)
		_, _ = traefik.ApplyAppRoute(ctx, traefik.AppOptions{
			Writer:          s.TraefikW,
			Client:          s.Cli,
			AppName:         appName,
			Spec:            spec,
			Domains:         domains,
			PublicHost:      s.Cfg.PublicHost,
			AppPathPrefix:   s.Cfg.AppPathPrefix,
			RootlessGateway: s.Cfg.RootlessGateway,
		})
	}(a.ID, a.Name)
	http.Redirect(w, r, "/apps/"+a.Name, http.StatusSeeOther)
}
