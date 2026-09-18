package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

type settingsData struct {
	PageData
	Username        string
	Creds           []*store.GitCredential
	Tokens          []*store.APIToken
	NewToken        string // raw value, rendered exactly once after creation
	NewTokenName    string
	SocketPath      string
	TraefikDir      string
	CertResolver    string
	PublicHost      string
	AppPathPrefix   string
	SelfRouteExists bool
	SelfRouteURL    string
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

// renderSettingsNewToken renders the settings page immediately after a
// token was minted, so the raw value is displayed exactly once.
func (s *Server) renderSettingsNewToken(w http.ResponseWriter, r *http.Request, raw, name string) {
	d := s.settingsPageData(r)
	d.NewToken = raw
	d.NewTokenName = name
	s.Renderer.Render(w, r, "settings.html", d)
}

func (s *Server) settingsPageData(r *http.Request) settingsData {
	creds, _ := s.Store.ListCredentials(r.Context())
	tokens, _ := s.Store.ListAPITokens(r.Context())
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
		PageData:        pageCtx(r, "Settings"),
		Username:        username,
		Creds:           creds,
		Tokens:          tokens,
		SocketPath:      s.Cfg.SocketPath,
		TraefikDir:      s.Cfg.TraefikDir,
		CertResolver:    s.Cfg.CertResolver,
		PublicHost:      s.Cfg.PublicHost,
		AppPathPrefix:   s.Cfg.AppPathPrefix,
		SelfRouteExists: exists,
		SelfRouteURL:    c.SelfURL(),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "")
}

// handleSettingsTokens mints a PAT and re-renders the settings page
// with the raw value shown once. Redirecting with the token in the URL
// would leak it into history and logs, so the page is the delivery.
func (s *Server) handleSettingsTokens(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderSettings(w, r, "Token name required.")
		return
	}
	if len(name) > 64 {
		s.renderSettings(w, r, "Token name too long (max 64 characters).")
		return
	}
	expires, err := tokenExpiryFromForm(r.FormValue("expiry"))
	if err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	tok, raw, err := s.Store.MintAPIToken(r.Context(), name, expires)
	if err != nil {
		s.renderSettings(w, r, "Could not create token: "+err.Error())
		return
	}
	s.recordAudit(r, audit.ActionTokenCreate, "token", tok.ID, tok.Name, audit.OutcomeSuccess, "prefix "+tok.Prefix)
	s.renderSettingsNewToken(w, r, raw, name)
}

func (s *Server) handleSettingsTokenDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteAPIToken(r.Context(), id); err != nil {
		s.redirectErr(w, r, "/settings", err.Error())
		return
	}
	s.recordAudit(r, audit.ActionTokenRevoke, "token", id, "", audit.OutcomeSuccess, "")
	s.redirectOK(w, r, "/settings", "API token revoked.")
}

// tokenExpiryFromForm maps the UI's day choices to an optional expiry.
func tokenExpiryFromForm(v string) (*time.Time, error) {
	days, err := strconv.Atoi(v)
	if err != nil || days < 0 {
		return nil, fmt.Errorf("invalid expiry choice %q", v)
	}
	if days == 0 {
		return nil, nil
	}
	t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	return &t, nil
}

func (s *Server) handleSettingsCreds(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	host := r.FormValue("host")
	username := r.FormValue("username")
	token := r.FormValue("token")
	if host == "" || token == "" {
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
