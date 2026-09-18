package web

import (
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// minPasswordLen matches the /setup policy so the CLI and web paths
// can't produce accounts the other path would reject.
const minPasswordLen = 8

// currentUser resolves the account behind the request's session.
// The authed group guarantees a session exists, so a miss here is a
// server-side inconsistency, not a client problem.
func (s *Server) currentUser(r *http.Request) (*store.User, error) {
	sess := sessionFromCtx(r.Context())
	if sess == nil {
		return nil, errors.New("no session")
	}
	return s.Store.GetUserByID(r.Context(), sess.UserID)
}

// handleAccountUsername renames the account after re-checking the
// current password, so a hijacked tab can't silently relabel the login.
func (s *Server) handleAccountUsername(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	u, err := s.currentUser(r)
	if err != nil {
		s.renderSettings(w, r, "no user")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(r.FormValue("current_password"))) != nil {
		s.recordAudit(r, audit.ActionUsernameChange, "user", u.ID, u.Username, audit.OutcomeFailure, "current password incorrect")
		s.renderSettings(w, r, "Username not changed: current password is incorrect.")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		s.renderSettings(w, r, "Username not changed: new username is empty.")
		return
	}
	if len(username) > 64 {
		s.renderSettings(w, r, "Username not changed: keep it under 64 characters.")
		return
	}
	if username == u.Username {
		s.redirectOK(w, r, "/settings", "")
		return
	}
	if err := s.Store.UpdateUsername(r.Context(), u.ID, username); err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			s.renderSettings(w, r, "Username not changed: that username is already taken.")
			return
		}
		s.renderSettings(w, r, err.Error())
		return
	}
	s.recordAudit(r, audit.ActionUsernameChange, "user", u.ID, username, audit.OutcomeSuccess, "was "+u.Username)
	s.redirectOK(w, r, "/settings", "Username updated.")
}

// handleAccountPassword rotates the password. Every other session is
// invalidated so stolen cookies die with the old credential; the
// current session survives so the actor isn't logged out mid-change.
func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	u, err := s.currentUser(r)
	if err != nil {
		s.renderSettings(w, r, "no user")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(r.FormValue("current_password"))) != nil {
		s.recordAudit(r, audit.ActionPasswordChange, "user", u.ID, u.Username, audit.OutcomeFailure, "current password incorrect")
		s.renderSettings(w, r, "Password not changed: current password is incorrect.")
		return
	}
	newPwd := r.FormValue("new_password")
	if len(newPwd) < minPasswordLen {
		s.renderSettings(w, r, "Password not changed: it must be at least 8 characters.")
		return
	}
	if newPwd != r.FormValue("confirm") {
		s.renderSettings(w, r, "Password not changed: the two new passwords don't match.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	if err := s.Store.UpdateUserPassword(r.Context(), u.ID, string(hash)); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}
	_ = s.Store.DeleteOtherSessionsForUser(r.Context(), u.ID, sessionFromCtx(r.Context()).ID)
	s.recordAudit(r, audit.ActionPasswordChange, "user", u.ID, u.Username, audit.OutcomeSuccess, "other sessions signed out")
	s.redirectOK(w, r, "/settings", "Password updated; other sessions were signed out.")
}
