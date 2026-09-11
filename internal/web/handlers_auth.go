package web

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"
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
		s.Renderer.Render(w, r, "setup.html", authData{PageData: PageData{Title: "Welcome"}, Error: err.Error()})
		return
	}
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
	if !s.logins.allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.Renderer.Render(w, r, "login.html", authData{
			PageData: PageData{Title: "Sign in"},
			Error:    "Too many failed attempts. Try again in a few minutes.",
		})
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	u, err := s.Store.GetUserByUsername(r.Context(), username)
	if err != nil {
		// Burn the same bcrypt cost as the success path.
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		s.logins.fail(ip)
		s.Renderer.Render(w, r, "login.html", authData{PageData: PageData{Title: "Sign in"}, Error: invalidPasswordMsg()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.logins.fail(ip)
		s.Renderer.Render(w, r, "login.html", authData{PageData: PageData{Title: "Sign in"}, Error: invalidPasswordMsg()})
		return
	}
	s.logins.success(ip)
	if err := s.newSession(r.Context(), r, w, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, _ := r.Cookie(sessionCookieNm); c != nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if sess := sessionFromCtx(r.Context()); sess != nil {
		_ = s.Store.DeleteSessionsForUser(r.Context(), sess.UserID)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
