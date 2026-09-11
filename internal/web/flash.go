package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// One-shot flash messages: a handler that fails (or succeeds) sets a
// short-lived cookie and redirects, and the flash middleware picks it up
// on the next page render so the base layout can show it as a toast.
// This replaces the old behavior of http.Error() leaving the browser on
// a bare plain-text page after a form POST.
const flashCookie = "se_flash"

const (
	flashError   = "e"
	flashSuccess = "s"
)

type flashKey struct{}

type flashMessage struct {
	Kind string
	Text string
}

func encodeFlash(kind, text string) string {
	return kind + ":" + text
}

func decodeFlash(v string) flashMessage {
	kind, text, ok := strings.Cut(v, ":")
	if !ok {
		return flashMessage{Kind: flashError, Text: v}
	}
	return flashMessage{Kind: kind, Text: text}
}

func (s *Server) setFlash(w http.ResponseWriter, kind, text string) {
	if text == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    url.QueryEscape(encodeFlash(kind, text)),
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.Cfg.InsecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30,
	})
}

// redirectErr flashes an error and sends the browser back to a real page
// (usually the app detail view) instead of rendering bare error text.
func (s *Server) redirectErr(w http.ResponseWriter, r *http.Request, to, msg string) {
	s.setFlash(w, flashError, msg)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// redirectOK flashes a success message and redirects.
func (s *Server) redirectOK(w http.ResponseWriter, r *http.Request, to, msg string) {
	s.setFlash(w, flashSuccess, msg)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// flashMessages consumes the flash cookie into the request context and
// clears it on the response. It runs in the authed group, before any
// handler renders a page.
func (s *Server) flashMessages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(flashCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		raw, decErr := url.QueryUnescape(c.Value)
		if decErr != nil {
			raw = c.Value
		}
		ctx := contextWithFlash(r.Context(), decodeFlash(raw))
		// Clear it so a refresh doesn't replay the toast.
		http.SetCookie(w, &http.Cookie{
			Name:     flashCookie,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   !s.Cfg.InsecureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func flashFrom(r *http.Request) flashMessage {
	if f, ok := r.Context().Value(flashKey{}).(flashMessage); ok {
		return f
	}
	return flashMessage{}
}

func contextWithFlash(ctx context.Context, f flashMessage) context.Context {
	return context.WithValue(ctx, flashKey{}, f)
}
