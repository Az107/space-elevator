package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// CSRF issues per-session tokens: HMAC-SHA256(serverKey, sessionID).
// The server key is a random 32-byte secret persisted in the state dir,
// so tokens survive restarts but cannot be forged without it. Hosted
// sibling-subdomain apps are same-site (SameSite=Lax won't help), so a
// bearer token the attacker's JS can never read is the reliable defense.
type CSRF struct {
	key []byte
}

const csrfKeyFile = "csrf.key"

func loadCSRFKey(stateDir string) (*CSRF, error) {
	path := filepath.Join(stateDir, csrfKeyFile)
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return &CSRF{key: b}, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return &CSRF{key: key}, nil
}

func (c *CSRF) Token(sessionID string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *CSRF) Verify(sessionID, token string) bool {
	if token == "" {
		return false
	}
	return hmac.Equal([]byte(c.Token(sessionID)), []byte(token))
}

// csrfProtect requires a valid token on every state-changing request in
// the authed group. It must run after RequireAuth (needs the session).
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		sess := sessionFromCtx(r.Context())
		if sess == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			contentType := r.Header.Get("Content-Type")
			switch {
			case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
				token = r.PostFormValue("csrf")
			case strings.HasPrefix(contentType, "multipart/form-data"):
				// The browser-rendered deploy form is multipart even when
				// deploying from git, so its hidden csrf field is not an
				// urlencoded body. Parse it here before the handler reads
				// the archive; the handler's later ParseMultipartForm call
				// reuses the parsed form and uploaded file.
				if err := r.ParseMultipartForm(32 << 20); err == nil {
					token = r.FormValue("csrf")
				}
			}
		}
		if !s.CSRF.Verify(sess.ID, token) {
			http.Error(w, "CSRF token missing or invalid", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
