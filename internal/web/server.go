package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	stdlog "log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

// maxRequestBody caps every request body (uploads included). The tar
// extraction itself has additional uncompressed-size limits.
const maxRequestBody = 128 << 20

type Server struct {
	Cfg       *config.Config
	Store     *store.Store
	Renderer  *Renderer
	Cli       *podman.Client
	Runtime   *composer.Runtime
	Deployer  *deployer.Deployer
	TraefikW  *traefik.Writer
	CSRF      *CSRF
	Audit     *audit.Logger
	BuildLogs *buildLogRegistry
	logins    *loginLimiter
	deploySem chan struct{}
}

func NewServer(cfg *config.Config) (*Server, error) {
	cfg.EnsureDirs()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return nil, err
	}
	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return nil, err
	}
	r, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	csrf, err := loadCSRFKey(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)
	tw := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	au := audit.New(st, os.Stderr)
	dep := deployer.New(st, rt, cli, tw, deployer.Options{
		AppsRoot:        cfg.AppsRoot,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
		CertResolver:    cfg.CertResolver,
		Audit:           au,
	})
	return &Server{
		Cfg:       cfg,
		Store:     st,
		Renderer:  r,
		Cli:       cli,
		Runtime:   rt,
		Deployer:  dep,
		TraefikW:  tw,
		CSRF:      csrf,
		Audit:     au,
		BuildLogs: newBuildLogRegistry(),
		logins:    newLoginLimiter(),
		deploySem: make(chan struct{}, 2),
	}, nil
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	// UI tweaks ship in the binary; make browsers revalidate statics
	// instead of heuristically keeping an old stylesheet next to new HTML.
	staticServer := http.StripPrefix("/static/", withCacheHeaders(http.FileServer(Static())))
	r.Get("/static/*", staticServer.ServeHTTP)
	r.Get("/static", staticServer.ServeHTTP)
	r.Get("/login", s.handleLoginForm)
	r.Post("/login", s.handleLoginSubmit)
	r.Get("/setup", s.handleSetupForm)
	r.Post("/setup", s.handleSetupSubmit)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
	})
	// PWA plumbing must live at the site root: a service worker
	// controls the scope of its URL directory (so /sw.js, not
	// /static/sw.js), and the manifest's scope derives from its own
	// URL unless "scope" is set. Served from the embedded statics.
	r.Get("/sw.js", s.serveStaticRoot("sw.js"))
	r.Get("/manifest.json", s.serveStaticRoot("manifest.json"))

	// REST API: bearer-token auth (no cookies, no CSRF). Wrapped by the
	// same outer limitBody/securityHeaders middleware as the UI.
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.requireAPIToken)
		r.Get("/apps", s.apiListApps)
		r.Post("/apps", s.apiCreateApp)
		r.Post("/apps/upload", s.apiUploadApp)
		r.Get("/apps/{name}", s.apiGetApp)
		r.Post("/apps/{name}/redeploy", s.apiRedeployApp)
		r.Post("/apps/{name}/restart", s.apiRestartApp)
		r.Post("/apps/{name}/start", s.apiStartApp)
		r.Post("/apps/{name}/stop", s.apiStopApp)
		r.Post("/apps/{name}/rename", s.apiRenameApp)
		r.Delete("/apps/{name}", s.apiDeleteApp)
		// Logs reuse the CLI-style text endpoint (no SSE for API v1).
		r.Get("/apps/{name}/logs", s.handleLogs)
		r.Put("/apps/{name}/env", s.apiPutAppEnv)
		r.Put("/apps/{name}/secrets", s.apiPutAppSecrets)
		r.Delete("/apps/{name}/secrets/{key}", s.apiDeleteSecret)
		r.Post("/apps/{name}/domains", s.apiAddDomain)
		r.Delete("/apps/{name}/domains/{domain}", s.apiRemoveDomain)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Use(s.csrfProtect)
		r.Use(s.flashMessages)
		r.Post("/logout", s.handleLogout)
		r.Post("/logout/all", s.handleLogoutAll)
		r.Get("/apps", s.handleApps)
		r.Get("/apps/new", s.handleDeployForm)
		r.Post("/apps/new", s.handleDeploySubmit)
		r.Post("/apps/drop", s.handleDrop)
		r.Get("/apps/{name}", s.handleAppDetail)
		r.Post("/apps/{name}/restart", s.handleAppRestart)
		r.Post("/apps/{name}/start", s.handleAppStart)
		r.Post("/apps/{name}/stop", s.handleAppStop)
		r.Post("/apps/{name}/rename", s.handleAppRename)
		r.Post("/apps/{name}/redeploy", s.handleAppRedeploy)
		r.Post("/apps/{name}/remove", s.handleAppRemove)
		r.Get("/apps/{name}/logs", s.handleLogs)
		r.Get("/apps/{name}/deploy-status", s.handleDeployStatus)
		r.Get("/apps/{name}/files", s.handleAppFiles)
		r.Get("/apps/{name}/file", s.handleAppFileView)
		r.Post("/apps/{name}/domains", s.handleDomainAdd)
		r.Post("/apps/{name}/domains/{domain}/delete", s.handleDomainRemove)
		r.Post("/apps/{name}/env", s.handleAppEnvSave)
		r.Post("/apps/{name}/secrets", s.handleAppSecretSet)
		r.Post("/apps/{name}/secrets/{key}/delete", s.handleAppSecretDelete)
		r.Get("/settings", s.handleSettings)
		r.Post("/settings/creds", s.handleSettingsCreds)
		r.Post("/settings/tokens", s.handleSettingsTokens)
		r.Post("/settings/tokens/{id}/delete", s.handleSettingsTokenDelete)
		r.Post("/settings/account/username", s.handleAccountUsername)
		r.Post("/settings/account/password", s.handleAccountPassword)
	})

	return securityHeaders(limitBody(s.originCheck(r)))
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return RequireAuth(s, next.ServeHTTP)
}

// securityHeaders applies defensive response headers to every response.
// frame-ancestors stays 'self' because the files viewer is embedded via
// an iframe on the app detail page.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"font-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// limitBody caps the size of every request body.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// originCheck blocks cross-site state-changing requests from browsers.
// The primary signal is Fetch Metadata (Sec-Fetch-Site): browsers attest
// where the request originated and cannot lie about it. This survives
// quirks like Safari and Chrome sending `Origin: null` from a login
// form after HTTP→HTTPS redirect chains (seen in production through
// Cloudflare). Browsers without Sec-Fetch-Site fall back to the
// Origin-host check (request host + configured public host, for
// setups where a proxy rewrites the forwarded Host).
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		switch r.Header.Get("Sec-Fetch-Site") {
		case "same-origin", "same-site", "none":
			next.ServeHTTP(w, r)
			return
		case "cross-site":
			log.Printf("origin-check: rejected fetch-site=cross-site origin=%q host=%q path=%s", origin, r.Host, r.URL.Path)
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		// No Fetch Metadata (legacy client): judge by Origin.
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			log.Printf("origin-check: rejected unparsable origin=%q host=%q path=%s", origin, r.Host, r.URL.Path)
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if strings.EqualFold(u.Host, r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		// Accepted only because it matches the configured public host —
		// log it so a rewiring proxy/outside-URL user shows up here and
		// once the setup is understood this can be tightened.
		if strings.EqualFold(u.Host, s.Cfg.PublicHost) {
			log.Printf("origin-check: allowed via public-host match origin=%q host=%q path=%s", origin, r.Host, r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}
		log.Printf("origin-check: rejected origin=%q host=%q path=%s", origin, r.Host, r.URL.Path)
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
	})
}

// log is the server's standard error logger (picked up by journalctl
// under the systemd user unit).
var log = stdlog.New(os.Stderr, "", stdlog.LstdFlags)

// withCacheHeaders sets caching for static assets. Assets referenced
// with a content-hash version (?v=…) are safe to cache forever — a
// changed file produces a changed URL. Unversioned requests revalidate
// so a deploy never leaves browsers with a mismatched page/asset pair.
// serveStaticRoot serves an embedded static file at the site root.
func (s *Server) serveStaticRoot(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := Static().Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		switch {
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(name, ".json"):
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		// no-cache: browsers re-check for SW updates on every visit.
		w.Header().Set("Cache-Control", "no-cache")
		if fi, err := f.Stat(); err == nil {
			http.ServeContent(w, r, name, fi.ModTime(), f)
			return
		}
		http.NotFound(w, r)
	}
}

func withCacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie stores a session cookie. Exposed for handlers.
func (s *Server) SetSessionCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieNm,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.Cfg.InsecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// rotateSession deletes the session presented by the client, if any, so
// a login never extends an existing session's life.
func (s *Server) rotateSession(r *http.Request) {
	if c, _ := r.Cookie(sessionCookieNm); c != nil && c.Value != "" {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
}

func (s *Server) createUserAndLogin(ctx context.Context, r *http.Request, w http.ResponseWriter, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := &store.User{ID: uuid.NewString(), Username: username, PasswordHash: string(hash)}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		return err
	}
	return s.newSession(ctx, r, w, u.ID)
}

// newSession mints a fresh 256-bit session token and stores only its
// SHA-256 hash.
func (s *Server) newSession(ctx context.Context, r *http.Request, w http.ResponseWriter, userID string) error {
	s.rotateSession(r)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	sess := &store.Session{
		ID:        base64.RawURLEncoding.EncodeToString(raw),
		UserID:    userID,
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return err
	}
	s.SetSessionCookie(w, sess.ID)
	return nil
}

func invalidPasswordMsg() string { return "Password incorrect." }
func setupMismatchMsg() string   { return "Passwords don't match." }
