package web

import (
	"context"
	"net/http"
	"time"

	"github.com/albertoruiz/space-elevator/internal/store"
)

type ctxKey string

const (
	ctxSession ctxKey = "session"
	ctxCSRF    ctxKey = "csrf"
)

// Session lifetime policy: absolute cap plus an idle window. Every
// authenticated request may slide the idle window forward (throttled to
// one DB write per hour), so an actively used session lives out its
// absolute TTL while an abandoned one dies after 24h.
const (
	sessionTTL      = 7 * 24 * time.Hour
	sessionIdle     = 24 * time.Hour
	sessionTouch    = 1 * time.Hour
	sessionCookieNm = "sid"
)

func sessionFromCtx(ctx context.Context) *store.Session {
	if v, ok := ctx.Value(ctxSession).(*store.Session); ok {
		return v
	}
	return nil
}

// sessionFromCookie resolves and validates the session cookie. Returns
// nil for missing, unknown, expired, or idle-timed-out sessions.
func sessionFromCookie(s *Server, r *http.Request) *store.Session {
	c, err := r.Cookie(sessionCookieNm)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := s.Store.GetSession(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	now := time.Now()
	if sess.ExpiresAt.Before(now) || now.Sub(sess.LastSeenAt) > sessionIdle {
		return nil
	}
	return sess
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieNm, Value: "", MaxAge: -1, Path: "/"})
}

// RequireAuth wraps a handler. If no admin user exists yet, redirect to /setup.
// Otherwise require a valid session cookie.
func RequireAuth(s *Server, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, _ := s.Store.CountUsers(r.Context())
		if n == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(sessionCookieNm)
		if err != nil || c.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		sess, err := s.Store.GetSession(r.Context(), c.Value)
		if err != nil {
			clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		now := time.Now()
		if sess.ExpiresAt.Before(now) || now.Sub(sess.LastSeenAt) > sessionIdle {
			_ = s.Store.DeleteSession(r.Context(), sess.ID)
			clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if now.Sub(sess.LastSeenAt) > sessionTouch {
			_ = s.Store.TouchSession(r.Context(), sess.ID, now)
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		ctx = context.WithValue(ctx, ctxCSRF, s.CSRF.Token(sess.ID))
		h.ServeHTTP(w, r.WithContext(ctx))
	}
}

// OptionalAuth sets a flag in the context but doesn't redirect. Used for /login.
func OptionalAuth(s *Server, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess := sessionFromCookie(s, r); sess != nil {
			ctx := context.WithValue(r.Context(), ctxSession, sess)
			r = r.WithContext(ctx)
		}
		h.ServeHTTP(w, r)
	})
}
