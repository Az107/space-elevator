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

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	n, _ := s.Store.CountUsers(r.Context())
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.Renderer.Render(w, "setup.html", authData{Error: ""})
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	pwd := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if pwd != confirm {
		s.Renderer.Render(w, "setup.html", authData{Error: setupMismatchMsg()})
		return
	}
	if len(pwd) < 8 {
		s.Renderer.Render(w, "setup.html", authData{Error: "Password must be at least 8 characters."})
		return
	}
	if err := s.createUserAndLogin(r.Context(), w, "admin", pwd); err != nil {
		s.Renderer.Render(w, "setup.html", authData{Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if c, _ := r.Cookie("sid"); c != nil {
		if _, err := s.Store.GetSession(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/apps", http.StatusSeeOther)
			return
		}
	}
	s.Renderer.Render(w, "login.html", authData{Error: ""})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	u, err := s.Store.GetUserByUsername(r.Context(), username)
	if err != nil {
		s.Renderer.Render(w, "login.html", authData{Error: invalidPasswordMsg()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.Renderer.Render(w, "login.html", authData{Error: invalidPasswordMsg()})
		return
	}
	if err := s.newSession(r.Context(), w, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, _ := r.Cookie("sid"); c != nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}