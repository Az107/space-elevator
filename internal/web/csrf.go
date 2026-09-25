package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CSRF issues per-session tokens: HMAC-SHA256(serverKey, sessionID).
// The server key is a random 32-byte secret persisted in the state dir,
// so tokens survive restarts but cannot be forged without it. Hosted
// sibling-subdomain apps are same-site (SameSite=Lax won't help), so a
// bearer token the attacker's JS can never read is the reliable defense.
type CSRF struct {
	key []byte
}

const (
	csrfKeyFile         = "csrf.key"
	anonymousCSRFCookie = "csrf_anon"
)

func loadCSRFKey(stateDir string) (*CSRF, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, csrfKeyFile)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlinked CSRF key %q", path)
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("invalid CSRF key length in %q", path)
		}
		return &CSRF{key: b}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(stateDir, ".csrf-key-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
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

// ensureAnonymousCSRF returns a form token bound to a short-lived,
// HttpOnly pre-auth cookie. Login and setup are outside the authenticated
// CSRF middleware, but still need a token so a sibling same-site app cannot
// submit a login/setup request in the victim's browser.
func (s *Server) ensureAnonymousCSRF(w http.ResponseWriter, r *http.Request) string {
	if s.CSRF == nil {
		// Lightweight test servers may omit the middleware. Production
		// servers always fail startup if the CSRF key cannot be loaded.
		return ""
	}
	subject := ""
	if c, err := r.Cookie(anonymousCSRFCookie); err == nil {
		subject = c.Value
	}
	if subject == "" || len(subject) > 128 {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			// A failed random source must not silently fall back to a
			// predictable token. Returning an empty value makes the form
			// unusable and the request fail closed.
			return ""
		}
		subject = hex.EncodeToString(raw)
		http.SetCookie(w, &http.Cookie{
			Name:     anonymousCSRFCookie,
			Value:    subject,
			Path:     "/",
			HttpOnly: true,
			Secure:   !s.Cfg.InsecureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int((30 * time.Minute).Seconds()),
		})
	}
	return s.CSRF.Token(subject)
}

func (s *Server) clearAnonymousCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: anonymousCSRFCookie, Value: "", Path: "/", MaxAge: -1})
}

func (s *Server) verifyAnonymousCSRF(w http.ResponseWriter, r *http.Request) bool {
	if s.CSRF == nil {
		return true
	}
	cookie, err := r.Cookie(anonymousCSRFCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf")
	}
	return s.CSRF.Verify(cookie.Value, token)
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
