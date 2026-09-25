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
		if ctx.Err() != nil {
			return
		}
		s.reconcileApp(ctx, a)
	}
}

// appRouteName is the Traefik file stem for an app. Slug is the stable
// runtime identity; pre-slug rows fall back to the display name.
func appRouteName(a *store.App) string {
	if a.Slug != "" {
		return a.Slug
	}
	return a.Name
}

// removeAppRoute deletes an app's dynamic file, logging (never failing on)
// removal errors — reconciliation is best effort and a later explicit
// route/domain command will surface a persistent filesystem problem.
func (s *Server) removeAppRoute(ctx context.Context, a *store.App, reason string) {
	if s.TraefikW == nil {
		return
	}
	if err := s.TraefikW.Remove(appRouteName(a)); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: remove route %s (%s): %v\n", a.Name, reason, err)
	}
}

// lastOperationFailedUnrecovered reports whether the app's most recent
// operation ended in a state that leaves the running containers
// untrustworthy: a plain failure, an interruption by a service restart, or
// a rollback that itself failed. A "rolled_back" status is deliberately not
// included — that state restores the previous release and is safe to route.
func (s *Server) lastOperationFailedUnrecovered(ctx context.Context, appID string) bool {
	operations, err := s.Store.ListOperations(ctx, appID)
	if err != nil || len(operations) == 0 {
		return false
	}
	switch operations[0].Status {
	case store.OperationStatusFailed, store.OperationStatusInterrupted, store.OperationStatusRollbackFailed:
		return true
	default:
		return false
	}
}

func (s *Server) reconcileApp(ctx context.Context, a *store.App) {
	spec, err := composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		// Nothing usable to route; make sure no stale file lingers.
		s.removeAppRoute(ctx, a, "unparsable compose")
		return
	}

	// An app left in "error" whose last operation did not recover is the
	// exact case where containers may still be the *candidate* release (or
	// half of it). Publishing a route there would expose an unverified
	// build, so keep the app dark until the operator retries.
	if a.Status == "error" && s.lastOperationFailedUnrecovered(ctx, a.ID) {
		s.removeAppRoute(ctx, a, "unrecovered failed operation")
		return
	}

	meta := composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}
	status, services, err := s.Runtime.Status(ctx, meta, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %s status: %v\n", a.Name, err)
		return
	}
	if len(services) == 0 {
		s.removeAppRoute(ctx, a, "no containers")
		return
	}
	if status == "stopped" && a.Status == "running" {
		if err := s.Runtime.Start(ctx, meta); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: start %s: %v\n", a.Name, err)
		} else {
			_ = s.Store.UpdateAppStatus(ctx, a.ID, "running")
		}
	}
	if err := s.syncAppRoute(ctx, a); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: route %s: %v\n", a.Name, err)
	}
}
