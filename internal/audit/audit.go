// Package audit records security-relevant actions to two sinks: the
// persistent audit_events table (via the store) and a structured JSON
// line on stderr, which systemd captures into journald for shipping to
// a SIEM. Writing is best-effort: an audit failure is reported to
// stderr but never fails the request or deploy it describes.
package audit

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"time"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// ActorType classifies who performed an action.
const (
	ActorUser      = "user"
	ActorToken     = "token"
	ActorCLI       = "cli"
	ActorSystem    = "system"
	ActorAnonymous = "anonymous"
)

// Outcomes.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// Action names, grouped by area. Kept as constants so call sites and
// queries can't drift on spelling.
const (
	ActionSetup          = "setup.initialize"
	ActionLogin          = "auth.login"
	ActionLogout         = "auth.logout"
	ActionLogoutAll      = "auth.logout_all"
	ActionUsernameChange = "account.username_change"
	ActionPasswordChange = "account.password_change"
	ActionTokenCreate    = "token.create"
	ActionTokenRevoke    = "token.revoke"
	ActionCredentialSet  = "credential.upsert"

	ActionAppCreate   = "app.create"
	ActionAppUpload   = "app.upload"
	ActionAppDeploy   = "app.deploy"
	ActionAppRedeploy = "app.redeploy"
	ActionAppRemove   = "app.remove"
	ActionAppRename   = "app.rename"
	ActionAppStart    = "app.start"
	ActionAppStop     = "app.stop"
	ActionAppRestart  = "app.restart"

	ActionEnvUpdate    = "app.env.update"
	ActionSecretSet    = "app.secret.set"
	ActionSecretDelete = "app.secret.delete"
	ActionDomainAdd    = "app.domain.add"
	ActionDomainRemove = "app.domain.remove"

	ActionOriginReject = "security.origin_reject"
)

// Actor identifies who performed an action. ID is an opaque stable
// identifier (user/token UUID); Label is human-readable.
type Actor struct {
	Type  string
	ID    string
	Label string
}

// Event describes one action to record. Actor and request metadata are
// filled from the context when not set explicitly.
type Event struct {
	Actor      Actor
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	Outcome    string
	IP         string
	UserAgent  string
	Detail     string
}

// Store is the persistence dependency (satisfied by *store.Store).
type Store interface {
	RecordAudit(ctx context.Context, e *store.AuditEvent) error
}

// Logger writes audit events to the store and to a JSON stream.
type Logger struct {
	store Store
	out   io.Writer
}

// New returns a Logger. A nil writer defaults to stderr.
func New(st Store, w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{store: st, out: w}
}

// Record persists the event and emits its JSON line. It never returns
// an error: sink failures are downgraded to a stderr note.
func (l *Logger) Record(ctx context.Context, e Event) {
	if l == nil {
		return
	}
	if e.Actor.Type == "" {
		e.Actor = ActorFromCtx(ctx)
	}
	if e.IP == "" || e.UserAgent == "" {
		ip, ua := RequestFromCtx(ctx)
		if e.IP == "" {
			e.IP = ip
		}
		if e.UserAgent == "" {
			e.UserAgent = ua
		}
	}
	if e.Actor.Type == "" {
		e.Actor.Type = ActorSystem
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}

	ev := &store.AuditEvent{
		At:         time.Now(),
		ActorType:  e.Actor.Type,
		ActorID:    e.Actor.ID,
		ActorLabel: e.Actor.Label,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		TargetName: e.TargetName,
		Outcome:    e.Outcome,
		IP:         e.IP,
		UserAgent:  e.UserAgent,
		Detail:     e.Detail,
	}
	if err := l.store.RecordAudit(ctx, ev); err != nil {
		l.emit(ev, "store_error: "+err.Error())
	}
	l.emit(ev, "")
}

// wireEvent is the JSON shape written to stderr. It mirrors the table
// but flattens the actor for easier SIEM parsing.
type wireEvent struct {
	Time       string `json:"time"`
	Audit      bool   `json:"audit"`
	ActorType  string `json:"actor_type"`
	ActorID    string `json:"actor_id,omitempty"`
	ActorLabel string `json:"actor_label,omitempty"`
	Action     string `json:"action"`
	TargetType string `json:"target_type,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
	Outcome    string `json:"outcome"`
	IP         string `json:"ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (l *Logger) emit(e *store.AuditEvent, errMsg string) {
	b, err := json.Marshal(wireEvent{
		Time:       e.At.UTC().Format(time.RFC3339),
		Audit:      true,
		ActorType:  e.ActorType,
		ActorID:    e.ActorID,
		ActorLabel: e.ActorLabel,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		TargetName: e.TargetName,
		Outcome:    e.Outcome,
		IP:         e.IP,
		UserAgent:  e.UserAgent,
		Detail:     e.Detail,
		Error:      errMsg,
	})
	if err != nil {
		log.Printf("audit: marshal event: %v", err)
		return
	}
	_, _ = l.out.Write(append(b, '\n'))
}

type actorKey struct{}
type requestKey struct{}

// WithActor attaches the acting identity to ctx, for propagation into
// background goroutines (which cannot use the request context).
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFromCtx returns the actor attached by WithActor, or a zero Actor.
func ActorFromCtx(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{}
}

// WithRequest attaches client request metadata (IP, User-Agent).
func WithRequest(ctx context.Context, ip, ua string) context.Context {
	return context.WithValue(ctx, requestKey{}, [2]string{ip, ua})
}

// RequestFromCtx returns IP and User-Agent attached by WithRequest.
func RequestFromCtx(ctx context.Context) (ip, ua string) {
	if v, ok := ctx.Value(requestKey{}).([2]string); ok {
		return v[0], v[1]
	}
	return "", ""
}
