package web

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
)

type authData struct {
	PageData
	Error string
	// Username echoes back what was typed so a failed login doesn't
	// force retyping it. Never prefill this with a stored value: the
	// field must stay editable, and a rename must not lock anyone out.
	Username string
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

func (s *Server) authPageData(w http.ResponseWriter, r *http.Request, title, message, username string) authData {
	return authData{
		PageData: PageData{Title: title, CSRFToken: s.ensureAnonymousCSRF(w, r)},
		Error:    message,
		Username: username,
	}
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	n, _ := s.Store.CountUsers(r.Context())
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.Renderer.Render(w, r, "setup.html", s.authPageData(w, r, "Welcome", "", ""))
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	n, _ := s.Store.CountUsers(r.Context())
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.verifyAnonymousCSRF(w, r) {
		s.Renderer.Render(w, r, "setup.html", s.authPageData(w, r, "Welcome", "Invalid or expired form. Please try again.", ""))
		return
	}
	pwd := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if pwd != confirm {
		s.Renderer.Render(w, r, "setup.html", s.authPageData(w, r, "Welcome", setupMismatchMsg(), ""))
		return
	}
	if len(pwd) < 8 {
		s.Renderer.Render(w, r, "setup.html", s.authPageData(w, r, "Welcome", "Password must be at least 8 characters.", ""))
		return
	}
	if err := s.createUserAndLogin(r.Context(), r, w, "admin", pwd); err != nil {
		s.recordAudit(r, audit.ActionSetup, "user", "", "admin", audit.OutcomeFailure, err.Error())
		s.Renderer.Render(w, r, "setup.html", s.authPageData(w, r, "Welcome", err.Error(), ""))
		return
	}
	s.clearAnonymousCSRFCookie(w)
	s.recordAudit(r, audit.ActionSetup, "user", "", "admin", audit.OutcomeSuccess, "initial admin account created")
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if sessionFromCookie(s, r) != nil {
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
		return
	}
	s.Renderer.Render(w, r, "login.html", s.authPageData(w, r, "Sign in", "", ""))
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.verifyAnonymousCSRF(w, r) {
		s.Renderer.Render(w, r, "login.html", s.authPageData(w, r, "Sign in", "Invalid or expired form. Please try again.", r.FormValue("username")))
		return
	}
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
		s.Renderer.Render(w, r, "login.html", s.authPageData(w, r, "Sign in", "Too many failed attempts. Try again in a few minutes.", username))
		return
	}
	u, err := s.Store.GetUserByUsername(r.Context(), username)
	if err != nil {
		// Burn the same bcrypt cost as the success path.
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		s.logins.fail(ip)
		s.recordAudit(r, audit.ActionLogin, "user", "", username, audit.OutcomeFailure, "unknown username")
		s.Renderer.Render(w, r, "login.html", s.authPageData(w, r, "Sign in", invalidPasswordMsg(), username))
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.logins.fail(ip)
		s.recordAudit(r, audit.ActionLogin, "user", u.ID, u.Username, audit.OutcomeFailure, "incorrect password")
		s.Renderer.Render(w, r, "login.html", s.authPageData(w, r, "Sign in", invalidPasswordMsg(), username))
		return
	}
	s.logins.success(ip)
	if err := s.newSession(r.Context(), r, w, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.clearAnonymousCSRFCookie(w)
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
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			s.recordAudit(r, audit.ActionLogout, "user", "", "", audit.OutcomeFailure, err.Error())
			http.Error(w, "could not invalidate session", http.StatusInternalServerError)
			return
		}
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	s.recordAudit(r, audit.ActionLogoutAll, "user", "", "", audit.OutcomeSuccess, "all sessions invalidated")
	if sess := sessionFromCtx(r.Context()); sess != nil {
		if err := s.Store.DeleteSessionsForUser(r.Context(), sess.UserID); err != nil {
			s.recordAudit(r, audit.ActionLogoutAll, "user", "", "", audit.OutcomeFailure, err.Error())
			http.Error(w, "could not invalidate sessions", http.StatusInternalServerError)
			return
		}
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
