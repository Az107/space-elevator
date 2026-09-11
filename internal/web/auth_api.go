package web

import (
	"net/http"
	"strings"
	"time"
)

// requireAPIToken authenticates /api/v1 requests via a Personal Access
// Token in the Authorization header. Unlike browser routes there is no
// cookie and no CSRF: the token itself is the single credential, so it
// must be shown once by the generator and stored hashed (like
// sessions).
//
// A missing, unknown, or expired token yields 401 with
// WWW-Authenticate: Bearer; that also keeps probes from learning
// whether a token is "close" to valid.
func (s *Server) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			unauthorized(w)
			return
		}
		tok, err := s.Store.GetAPIToken(r.Context(), raw)
		if err != nil {
			unauthorized(w)
			return
		}
		// Throttled last-used tracking: at most one write per token per
		// minute keeps hot loops from churning the DB.
		now := time.Now()
		if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) > time.Minute {
			_ = s.Store.TouchAPIToken(r.Context(), tok.ID, now)
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="space-elevator-api"`)
	jsonError(w, http.StatusUnauthorized, "missing or invalid API token")
}
