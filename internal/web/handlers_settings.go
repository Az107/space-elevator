package web

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

type settingsData struct {
	PageData
	Creds         []*store.GitCredential
	SocketPath    string
	TraefikDir    string
	CertResolver  string
	PublicHost    string
	AppPathPrefix string
	SelfRouteExists bool
	SelfRouteURL  string
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	creds, _ := s.Store.ListCredentials(r.Context())
	exists, _ := s.TraefikW.SelfExists()
	c := traefik.SelfRouteConfig{
		Host:         s.Cfg.PublicHost,
		PathPrefix:   s.Cfg.PublicPath,
		BackendURL:   s.Cfg.DashboardURL,
		CertResolver: s.Cfg.CertResolver,
	}
	s.Renderer.Render(w, "settings.html", settingsData{
		PageData:        PageData{Authed: true},
		Creds:           creds,
		SocketPath:      s.Cfg.SocketPath,
		TraefikDir:      s.Cfg.TraefikDir,
		CertResolver:    s.Cfg.CertResolver,
		PublicHost:      s.Cfg.PublicHost,
		AppPathPrefix:   s.Cfg.AppPathPrefix,
		SelfRouteExists: exists,
		SelfRouteURL:    c.SelfURL(),
	})
}

func (s *Server) handleSettingsCreds(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	host := r.FormValue("host")
	username := r.FormValue("username")
	token := r.FormValue("token")
	if host == "" || token == "" {
		http.Error(w, "host + token required", http.StatusBadRequest)
		return
	}
	if username == "" {
		username = "x-access-token"
	}
	c := &store.GitCredential{
		ID:       uuid.NewString(),
		Host:     host,
		Username: username,
		Token:    token,
	}
	if err := s.Store.UpsertCredential(r.Context(), c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}