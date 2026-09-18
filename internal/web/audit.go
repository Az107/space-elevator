package web

import (
	"context"
	"net/http"

	"github.com/albertoruiz/space-elevator/internal/audit"
)

// auditActor resolves the acting identity for the current request: an
// API token (placed in context by requireAPIToken), else the session's
// user, else anonymous.
func (s *Server) auditActor(r *http.Request) audit.Actor {
	if a := audit.ActorFromCtx(r.Context()); a.Type != "" {
		return a
	}
	if sess := sessionFromCtx(r.Context()); sess != nil {
		label := ""
		if u, err := s.Store.GetUserByID(r.Context(), sess.UserID); err == nil {
			label = u.Username
		}
		return audit.Actor{Type: audit.ActorUser, ID: sess.UserID, Label: label}
	}
	return audit.Actor{Type: audit.ActorAnonymous}
}

// recordAudit is the web/API entry point for the audit trail. It pulls
// actor, IP, and User-Agent from the request and never fails the caller.
func (s *Server) recordAudit(r *http.Request, action, targetType, targetID, targetName, outcome, detail string) {
	s.Audit.Record(r.Context(), audit.Event{
		Actor:      s.auditActor(r),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		TargetName: targetName,
		Outcome:    outcome,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Detail:     detail,
	})
}

// backgroundAuditCtx captures the actor and client metadata from the
// request into a fresh context for work that outlives the request
// (async deploys), whose own context is cancelled when the handler
// returns.
func (s *Server) backgroundAuditCtx(r *http.Request) context.Context {
	ctx := audit.WithActor(context.Background(), s.auditActor(r))
	return audit.WithRequest(ctx, clientIP(r), r.UserAgent())
}
