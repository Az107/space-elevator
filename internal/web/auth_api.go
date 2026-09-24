package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/tokenmanager"
)

// requireAPIToken authenticates /api/v1 requests through the external
// Token-Manager service. Token-Manager is the only source of truth for token
// creation, expiry, revocation, and last-used tracking. If it cannot provide
// a trustworthy answer, authentication fails closed with 503; there is no
// local token fallback.
func (s *Server) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			unauthorized(w)
			return
		}
		if s.TokenManager == nil {
			authenticationUnavailable(w)
			return
		}

		tok, err := s.TokenManager.Validate(r.Context(), raw)
		if err != nil {
			if errors.Is(err, tokenmanager.ErrInvalidToken) {
				unauthorized(w)
			} else {
				authenticationUnavailable(w)
			}
			return
		}

		ctx := audit.WithActor(r.Context(), audit.Actor{
			Type:  audit.ActorToken,
			ID:    tok.ID,
			Label: tok.Name,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
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

func authenticationUnavailable(w http.ResponseWriter) {
	jsonError(w, http.StatusServiceUnavailable, "API authentication service unavailable")
}
