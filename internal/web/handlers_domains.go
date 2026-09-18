package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

// domainPattern matches a plain FQDN (no wildcards, no underscores —
// both would end up inside a Traefik router rule string).
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func validDomain(d string) bool {
	return len(d) <= 253 && domainPattern.MatchString(d)
}

// Sentinel errors surfaced with distinct HTTP statuses by the API.
var (
	errInvalidDomain  = errors.New("invalid domain")
	errDomainReserved = errors.New("domain is the dashboard host")
	errDomainTaken    = errors.New("domain already attached")
)

// attachDomain validates and attaches a domain to an app, then
// regenerates its Traefik route. Shared by the web form and the API.
func (s *Server) attachDomain(ctx context.Context, a *store.App, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return fmt.Errorf("domain required: %w", errInvalidDomain)
	}
	if !validDomain(domain) {
		return fmt.Errorf("invalid domain %q: expected a plain hostname like app.example.com: %w", domain, errInvalidDomain)
	}
	if domain == s.Cfg.PublicHost {
		return fmt.Errorf("that domain is the dashboard host and cannot be attached to an app: %w", errDomainReserved)
	}
	// A domain may only route to one app; attaching it twice would make
	// Traefik's routing ambiguous.
	if other, err := s.Store.ListDomain(ctx, domain); err == nil && other != nil && other.ID != a.ID {
		return fmt.Errorf("domain %q is already attached to app %q: %w", domain, other.Name, errDomainTaken)
	}
	current, _ := s.Store.GetAppDomains(ctx, a.ID)
	for _, d := range current {
		if d == domain {
			return nil // already attached; idempotent
		}
	}
	if err := s.Store.SetAppDomains(ctx, a.ID, append(current, domain)); err != nil {
		return err
	}
	s.regenerateTraefik(ctx, a)
	return nil
}

// detachDomain removes a domain (if present) and regenerates the
// app's Traefik route. Idempotent.
func (s *Server) detachDomain(ctx context.Context, a *store.App, domain string) error {
	current, _ := s.Store.GetAppDomains(ctx, a.ID)
	updated := make([]string, 0, len(current))
	changed := false
	for _, d := range current {
		if d == domain {
			changed = true
			continue
		}
		updated = append(updated, d)
	}
	if !changed {
		return nil
	}
	if err := s.Store.SetAppDomains(ctx, a.ID, updated); err != nil {
		return err
	}
	s.regenerateTraefik(ctx, a)
	return nil
}

func (s *Server) handleDomainAdd(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return
	}
	domain := strings.TrimSpace(r.FormValue("domain"))
	if err := s.attachDomain(r.Context(), a, domain); err != nil {
		s.recordAudit(r, audit.ActionDomainAdd, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionDomainAdd, "app", a.ID, a.Name, audit.OutcomeSuccess, "domain "+domain)
	s.redirectOK(w, r, "/apps/"+a.Name, fmt.Sprintf("Domain %s attached.", domain))
}

func (s *Server) handleDomainRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		s.redirectErr(w, r, "/apps", "app not found")
		return
	}
	domain := chi.URLParam(r, "domain")
	if err := s.detachDomain(r.Context(), a, domain); err != nil {
		s.recordAudit(r, audit.ActionDomainRemove, "app", a.ID, a.Name, audit.OutcomeFailure, err.Error())
		s.redirectErr(w, r, "/apps/"+a.Name, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionDomainRemove, "app", a.ID, a.Name, audit.OutcomeSuccess, "domain "+domain)
	s.redirectOK(w, r, "/apps/"+a.Name, fmt.Sprintf("Domain %s detached.", domain))
}

func (s *Server) regenerateTraefik(ctx context.Context, a *store.App) {
	domains, err := s.Store.GetAppDomains(ctx, a.ID)
	if err != nil {
		return
	}
	if len(domains) == 0 && (s.Cfg.PublicHost == "" || s.Cfg.AppPathPrefix == "") {
		_ = s.TraefikW.Remove(a.Slug)
		return
	}
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		return
	}
	_, _ = traefik.ApplyAppRoute(ctx, traefik.AppOptions{
		Writer:          s.TraefikW,
		Client:          s.Cli,
		AppName:         a.Slug,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      s.Cfg.PublicHost,
		AppPathPrefix:   s.Cfg.AppPathPrefix,
		RootlessGateway: s.Cfg.RootlessGateway,
	})
}
