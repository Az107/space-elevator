package web

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

type settingsData struct {
	PageData
	Username               string
	UsernameValue          string // what the username field shows (echoes a failed attempt)
	UsernameErr            string // inline error scoped to the username form
	PasswordErr            string // inline error scoped to the password form
	Creds                  []*store.GitCredential
	TokenManagerURL        string
	TokenManagerConfigured bool
	SocketPath             string
	TraefikDir             string
	CertResolver           string
	PublicHost             string
	AppPathPrefix          string
	SelfRouteExists        bool
	SelfRouteURL           string
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, errMsg string) {
	d := s.settingsPageData(r)
	// Only override the flash (picked up in pageCtx) when the handler
	// has a direct validation message to show.
	if errMsg != "" {
		d.Error = errMsg
	}
	s.Renderer.Render(w, r, "settings.html", d)
}

func (s *Server) settingsPageData(r *http.Request) settingsData {
	creds, _ := s.Store.ListCredentials(r.Context())
	exists, _ := s.TraefikW.SelfExists()
	c := traefik.SelfRouteConfig{
		Host:         s.Cfg.PublicHost,
		PathPrefix:   s.Cfg.PublicPath,
		BackendURL:   s.Cfg.DashboardURL,
		CertResolver: s.Cfg.CertResolver,
	}
	username := ""
	if sess := sessionFromCtx(r.Context()); sess != nil {
		if u, err := s.Store.GetUserByID(r.Context(), sess.UserID); err == nil {
			username = u.Username
		}
	}
	return settingsData{
		PageData:               pageCtx(r, "Settings"),
		Username:               username,
		UsernameValue:          username,
		Creds:                  creds,
		TokenManagerURL:        s.Cfg.TokenManagerURL,
		TokenManagerConfigured: s.TokenManager != nil,
		SocketPath:             s.Cfg.SocketPath,
		TraefikDir:             s.Cfg.TraefikDir,
		CertResolver:           s.Cfg.CertResolver,
		PublicHost:             s.Cfg.PublicHost,
		AppPathPrefix:          s.Cfg.AppPathPrefix,
		SelfRouteExists:        exists,
		SelfRouteURL:           c.SelfURL(),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "")
}

// renderAccountForm re-renders Settings with an error scoped to one of
// the two Account forms. Keeping the message on the form (instead of the
// page-level flash) puts it directly above the fields that produced it,
// so it can't read as belonging to the other form — and no redirect means
// what was typed survives. submitted is echoed back into the username
// field; password fields are never echoed.
func (s *Server) renderAccountForm(w http.ResponseWriter, r *http.Request, form, msg, submitted string) {
	d := s.settingsPageData(r)
	switch form {
	case "username":
		d.UsernameErr = msg
		d.UsernameValue = submitted
	case "password":
		d.PasswordErr = msg
	}
	s.Renderer.Render(w, r, "settings.html", d)
}

func (s *Server) handleSettingsCreds(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	host := strings.ToLower(strings.TrimSpace(r.FormValue("host")))
	username := strings.TrimSpace(r.FormValue("username"))
	token := r.FormValue("token")
	if host == "" || token == "" || strings.ContainsAny(host, "/@:\r\n\t ") || strings.ContainsRune(token, 0) {
		s.renderSettings(w, r, "Host and token are required.")
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
		s.renderSettings(w, r, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionCredentialSet, "git_credential", c.ID, host, audit.OutcomeSuccess, "username "+username)
	s.redirectOK(w, r, "/settings", "Git credential saved.")
}
