package web

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

type Server struct {
	Cfg       *config.Config
	Store     *store.Store
	Renderer  *Renderer
	Cli       *podman.Client
	Runtime   *composer.Runtime
	TraefikW  *traefik.Writer
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
	return &Server{
		Cfg:      cfg,
		Store:    st,
		Renderer: r,
		Cli:      cli,
		Runtime:  composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit),
		TraefikW: traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver),
	}, nil
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	staticServer := http.StripPrefix("/static/", http.FileServer(Static()))
	r.Get("/static/*", staticServer.ServeHTTP)
	r.Get("/static", staticServer.ServeHTTP)
	r.Get("/login", s.handleLoginForm)
	r.Post("/login", s.handleLoginSubmit)
	r.Get("/setup", s.handleSetupForm)
	r.Post("/setup", s.handleSetupSubmit)
	r.Post("/logout", s.handleLogout)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/apps", s.handleApps)
		r.Get("/apps/new", s.handleDeployForm)
		r.Post("/apps/new", s.handleDeploySubmit)
		r.Post("/apps/drop", s.handleDrop)
		r.Get("/apps/{name}", s.handleAppDetail)
		r.Post("/apps/{name}/restart", s.handleAppRestart)
		r.Post("/apps/{name}/redeploy", s.handleAppRedeploy)
		r.Post("/apps/{name}/remove", s.handleAppRemove)
		r.Get("/apps/{name}/logs", s.handleLogs)
		r.Get("/apps/{name}/files", s.handleAppFiles)
		r.Get("/apps/{name}/file", s.handleAppFileView)
		r.Post("/apps/{name}/domains", s.handleDomainAdd)
		r.Post("/apps/{name}/domains/{domain}/delete", s.handleDomainRemove)
		r.Get("/settings", s.handleSettings)
		r.Post("/settings/creds", s.handleSettingsCreds)
	})

	return r
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return RequireAuth(s, next.ServeHTTP)
}

// SetSessionCookie stores a session cookie. Exposed for handlers.
func (s *Server) SetSessionCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

func (s *Server) createUserAndLogin(ctx context.Context, w http.ResponseWriter, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := &store.User{ID: uuid.NewString(), Username: username, PasswordHash: string(hash)}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		return err
	}
	return s.newSession(ctx, w, u.ID)
}

func (s *Server) newSession(ctx context.Context, w http.ResponseWriter, userID string) error {
	sess := &store.Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return err
	}
	s.SetSessionCookie(w, sess.ID)
	return nil
}

func invalidPasswordMsg() string { return "Password incorrect." }
func setupMismatchMsg() string   { return "Passwords don't match." }