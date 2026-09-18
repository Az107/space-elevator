package web

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
)

type authData struct {
	PageData
	Error string
}

func (a authData) AuthedOK() bool   { return a.Authed }
func (a authData) ErrorStr() string { return a.Error }

// dummyBcryptHash equalizes the unknown-username path: comparing against
// it costs the same as a real comparison, so response timing doesn't
// reveal whether a username exists.
var dummyBcryptHash []byte

func init() {
	dummyBcryptHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	n, _ := s.Store.CountUsers(r.Context())
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.Renderer.Render(w, r, "setup.html", authData{PageData: PageData{Title: "Welcome"}, Error: ""})
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	n, _ := s.Store.CountUsers(r.Context())
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	pwd := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if pwd != confirm {
		s.Renderer.Render(w, r, "setup.html", authData{PageData: PageData{Title: "Welcome"}, Error: setupMismatchMsg()})
		return
	}
	if len(pwd) < 8 {
		s.Renderer.Render(w, r, "setup.html", authData{PageData: PageData{Title: "Welcome"}, Error: "Password must be at least 8 characters."})
		return
	}
	if err := s.createUserAndLogin(r.Context(), r, w, "admin", pwd); err != nil {
		s.recordAudit(r, audit.ActionSetup, "user", "", "admin", audit.OutcomeFailure, err.Error())
		s.Renderer.Render(w, r, "setup.html", authData{PageData: PageData{Title: "Welcome"}, Error: err.Error()})
		return
	}
	s.recordAudit(r, audit.ActionSetup, "user", "", "admin", audit.OutcomeSuccess, "initial admin account created")
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if sessionFromCookie(s, r) != nil {
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
		return
	}
	s.Renderer.Render(w, r, "login.html", authData{PageData: PageData{Title: "Sign in"}, Error: ""})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	username := r.FormValue("username")
	password := r.FormValue("password")
	if !s.logins.allow(ip) {
		s.Audit.Record(r.Context(), audit.Event{
			Actor:      audit.Actor{Type: audit.ActorAnonymous, Label: username},
			Action:     audit.ActionLogin,
			TargetType: "user",
			TargetName: username,
			Outcome:    audit.OutcomeFailure,
			IP:         ip,
			UserAgent:  r.UserAgent(),
			Detail:     "rate limited: too many failed attempts",
		})
		w.WriteHeader(http.StatusTooManyRequests)
		s.Renderer.Render(w, r, "login.html", authData{
			PageData: PageData{Title: "Sign in"},
			Error:    "Too many failed attempts. Try again in a few minutes.",
		})
		return
	}
	u, err := s.Store.GetUserByUsername(r.Context(), username)
	if err != nil {
		// Burn the same bcrypt cost as the success path.
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		s.logins.fail(ip)
		s.recordAudit(r, audit.ActionLogin, "user", "", username, audit.OutcomeFailure, "unknown username")
		s.Renderer.Render(w, r, "login.html", authData{PageData: PageData{Title: "Sign in"}, Error: invalidPasswordMsg()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.logins.fail(ip)
		s.recordAudit(r, audit.ActionLogin, "user", u.ID, u.Username, audit.OutcomeFailure, "incorrect password")
		s.Renderer.Render(w, r, "login.html", authData{PageData: PageData{Title: "Sign in"}, Error: invalidPasswordMsg()})
		return
	}
	s.logins.success(ip)
	if err := s.newSession(r.Context(), r, w, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		Actor:      audit.Actor{Type: audit.ActorUser, ID: u.ID, Label: u.Username},
		Action:     audit.ActionLogin,
		TargetType: "user",
		TargetID:   u.ID,
		TargetName: u.Username,
		Outcome:    audit.OutcomeSuccess,
		IP:         ip,
		UserAgent:  r.UserAgent(),
	})
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.recordAudit(r, audit.ActionLogout, "user", "", "", audit.OutcomeSuccess, "")
	if c, _ := r.Cookie(sessionCookieNm); c != nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	s.recordAudit(r, audit.ActionLogoutAll, "user", "", "", audit.OutcomeSuccess, "all sessions invalidated")
	if sess := sessionFromCtx(r.Context()); sess != nil {
		_ = s.Store.DeleteSessionsForUser(r.Context(), sess.UserID)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
