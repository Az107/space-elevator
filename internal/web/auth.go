package web

import (
	"context"
	"net/http"
	"time"
)

type ctxKey string

const (
	ctxSession ctxKey = "sid"
)

// RequireAuth wraps a handler. If no admin user exists yet, redirect to /setup.
// Otherwise require a valid session cookie.
func RequireAuth(s *Server, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, _ := s.Store.CountUsers(r.Context())
		if n == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie("sid")
		if err != nil || c.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		sess, err := s.Store.GetSession(r.Context(), c.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if sess.ExpiresAt.Before(time.Now()) {
			_ = s.Store.DeleteSession(r.Context(), sess.ID)
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess.ID)
		h.ServeHTTP(w, r.WithContext(ctx))
	}
}

// OptionalAuth sets a flag in the context but doesn't redirect. Used for /login.
func OptionalAuth(s *Server, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("sid")
		if c != nil {
			if sess, err := s.Store.GetSession(r.Context(), c.Value); err == nil {
				ctx := context.WithValue(r.Context(), ctxSession, sess.ID)
				r = r.WithContext(ctx)
			}
		}
		h.ServeHTTP(w, r)
	})
}