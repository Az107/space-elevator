package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

func (s *Server) handleDomainAdd(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	domain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	if domain == "" {
		http.Redirect(w, r, fmt.Sprintf("/apps/%s", name), http.StatusSeeOther)
		return
	}
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	current, _ := s.Store.GetAppDomains(r.Context(), a.ID)
	for _, d := range current {
		if d == domain {
			http.Redirect(w, r, fmt.Sprintf("/apps/%s", name), http.StatusSeeOther)
			return
		}
	}
	updated := append(current, domain)
	if err := s.Store.SetAppDomains(r.Context(), a.ID, updated); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.regenerateTraefik(r, a)
	http.Redirect(w, r, fmt.Sprintf("/apps/%s", name), http.StatusSeeOther)
}

func (s *Server) handleDomainRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	domain := chi.URLParam(r, "domain")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	current, _ := s.Store.GetAppDomains(r.Context(), a.ID)
	updated := make([]string, 0, len(current))
	for _, d := range current {
		if d != domain {
			updated = append(updated, d)
		}
	}
	if err := s.Store.SetAppDomains(r.Context(), a.ID, updated); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.regenerateTraefik(r, a)
	http.Redirect(w, r, fmt.Sprintf("/apps/%s", name), http.StatusSeeOther)
}

func (s *Server) regenerateTraefik(r *http.Request, a *store.App) {
	domains, err := s.Store.GetAppDomains(r.Context(), a.ID)
	if err != nil {
		return
	}
	if len(domains) == 0 && (s.Cfg.PublicHost == "" || s.Cfg.AppPathPrefix == "") {
		_ = s.TraefikW.Remove(a.Name)
		return
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		return
	}
	_, _ = traefik.ApplyAppRoute(r.Context(), traefik.AppOptions{
		Writer:          s.TraefikW,
		Client:          s.Cli,
		AppName:         a.Name,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      s.Cfg.PublicHost,
		AppPathPrefix:   s.Cfg.AppPathPrefix,
		RootlessGateway: s.Cfg.RootlessGateway,
	})
}