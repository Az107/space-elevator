package web

import (
	"context"
	"fmt"
	"os"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// ReconcileRoutes runs once at startup to heal route/runtime drift that
// would otherwise persist until a manual redeploy:
//
//   - a stale Traefik file for a dead container (Traefik 502), and
//   - a missing Traefik file for a live container (Traefik 404).
//
// An app whose stored status is "running" but whose containers were
// stopped externally (host event, podman restart, proxy cutover) is
// started again first. Every app's dynamic file is then rewritten — or
// removed — so it matches the live container state.
func (s *Server) ReconcileRoutes(ctx context.Context) {
	apps, err := s.Store.ListApps(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: list apps: %v\n", err)
		return
	}
	for _, a := range apps {
		s.reconcileApp(ctx, a)
	}
}

func (s *Server) reconcileApp(ctx context.Context, a *store.App) {
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		// Nothing usable to route; make sure no stale file lingers.
		_ = s.TraefikW.Remove(a.Slug)
		return
	}
	meta := composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}
	status, services, err := s.Runtime.Status(ctx, meta, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %s status: %v\n", a.Name, err)
		return
	}
	if len(services) == 0 {
		_ = s.TraefikW.Remove(a.Slug)
		return
	}
	if status == "stopped" && a.Status == "running" {
		if err := s.Runtime.Start(ctx, meta); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: start %s: %v\n", a.Name, err)
		} else {
			_ = s.Store.UpdateAppStatus(ctx, a.ID, "running")
		}
	}
	s.syncAppRoute(ctx, a)
}
