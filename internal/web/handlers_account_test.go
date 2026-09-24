package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

// accountTestServer builds a server with everything the Settings page
// renders (store, renderer, audit, Traefik writer for the self-route
// check, login limiter) plus one signed-in user.
func accountTestServer(t *testing.T) (*Server, *store.User) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{ID: "u1", Username: "admin", PasswordHash: string(hash)}
	if err := st.CreateUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return &Server{
		Store:    st,
		Renderer: r,
		Audit:    audit.New(st, &bytes.Buffer{}),
		TraefikW: traefik.NewWriter(t.TempDir(), "letsencrypt"),
		logins:   newLoginLimiter(),
		Cfg:      &config.Config{},
	}, u
}

// postAccountForm POSTs as the signed-in user. The account handlers
// render in place on validation errors (no redirect), which is what the
// scoping assertions below rely on; success returns 303.
func postAccountForm(t *testing.T, h http.HandlerFunc, u *store.User, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/settings/account/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), ctxSession,
		&store.Session{ID: "sess-1", UserID: u.ID}))
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

// inBand reports whether needle appears strictly between the two markers,
// so an error can be shown to land inside its own account block.
func inBand(body, start, end, needle string) bool {
	s := strings.Index(body, start)
	e := strings.Index(body, end)
	n := strings.Index(body, needle)
	return s >= 0 && e > s && n > s && n < e
}

// The login username must stay editable. It used to be hardcoded
// value="admin" readonly, so renaming the account left the form posting
// a name that no longer existed — permanently locking the operator out.
func TestLoginUsernameFieldEditable(t *testing.T) {
	s, _ := loginTestServer(t)
	w := httptest.NewRecorder()
	s.handleLoginForm(w, httptest.NewRequest("GET", "/login", nil))
	body := w.Body.String()
	if strings.Contains(body, "readonly") {
		t.Error("login username must not be readonly: renaming the account would lock the user out")
	}
	if !strings.Contains(body, `name="username"`) {
		t.Error("login page must render a username field")
	}
	if !strings.Contains(body, "autofocus") {
		t.Error("login page should focus the first field")
	}
}

// A failed login must keep what was typed instead of forcing a retype.
func TestFailedLoginEchoesTypedUsername(t *testing.T) {
	s, st := loginTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(t.Context(), &store.User{ID: "u1", Username: "alberto", PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"username": {"alberto"}, "password": {"nope"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.0.1:4242"
	w := httptest.NewRecorder()
	s.handleLoginSubmit(w, req)

	if w.Code == http.StatusOK && !strings.Contains(w.Body.String(), `value="alberto"`) {
		t.Error("typed username must survive a failed login")
	}
}

// A username error belongs to the Username block, not the Password block,
// and must not wipe what was submitted.
func TestUsernameChangeErrorScopedToForm(t *testing.T) {
	s, u := accountTestServer(t)
	w := postAccountForm(t, s.handleAccountUsername, u, url.Values{
		"current_password": {"wrong-password"},
		"username":         {"alberto"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (renders in place); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	const msg = "Username not changed: current password is incorrect."
	if !strings.Contains(body, msg) {
		t.Fatalf("error message missing from page; body: %s", body)
	}
	if !inBand(body, "<h3>Username</h3>", `action="/settings/account/username"`, msg) {
		t.Error("username error must render inside the Username block")
	}
	if inBand(body, "<h3>Password</h3>", `action="/settings/account/password"`, msg) {
		t.Error("username error must not appear in the Password block")
	}
	if !strings.Contains(body, `value="alberto"`) {
		t.Error("submitted username must be preserved on error")
	}
	if strings.Contains(body, "wrong-password") {
		t.Error("entered password must never be echoed back")
	}
}

// A password error belongs to the Password block, and no password value
// is ever echoed.
func TestPasswordChangeErrorScopedToForm(t *testing.T) {
	s, u := accountTestServer(t)
	w := postAccountForm(t, s.handleAccountPassword, u, url.Values{
		"current_password": {"wrong-password"},
		"new_password":     {"brand-new-secret"},
		"confirm":          {"brand-new-secret"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (renders in place); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	const msg = "Password not changed: current password is incorrect."
	if !strings.Contains(body, msg) {
		t.Fatalf("error message missing from page; body: %s", body)
	}
	if !inBand(body, "<h3>Password</h3>", `action="/settings/account/password"`, msg) {
		t.Error("password error must render inside the Password block")
	}
	if inBand(body, "<h3>Username</h3>", `action="/settings/account/username"`, msg) {
		t.Error("password error must not appear in the Username block")
	}
	if strings.Contains(body, "brand-new-secret") || strings.Contains(body, "wrong-password") {
		t.Error("passwords must never be echoed back")
	}
}

// Confirm mismatch is caught and reported on the form itself.
func TestPasswordConfirmMismatchReported(t *testing.T) {
	s, u := accountTestServer(t)
	w := postAccountForm(t, s.handleAccountPassword, u, url.Values{
		"current_password": {"correct-horse"},
		"new_password":     {"brand-new-secret"},
		"confirm":          {"something-else-entirely"},
	})
	body := w.Body.String()
	// Assert on a substring without the apostrophe: html/template may
	// entity-escape it in text context.
	if !strings.Contains(body, "two new passwords") {
		t.Errorf("mismatch error missing from page; body: %s", body)
	}
	if strings.Contains(body, "brand-new-secret") || strings.Contains(body, "something-else-entirely") {
		t.Error("passwords must never be echoed back")
	}
}

// Renaming the account must not lock anyone out: after a rename the
// login flow authenticates the new username.
func TestRenamedAccountCanLogIn(t *testing.T) {
	s, u := accountTestServer(t)
	w := postAccountForm(t, s.handleAccountUsername, u, url.Values{
		"current_password": {"correct-horse"},
		"username":         {"alberto"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("rename status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if got, err := s.Store.GetUserByID(t.Context(), u.ID); err != nil || got.Username != "alberto" {
		t.Fatalf("username = %q (err=%v), want alberto", func() string {
			if got != nil {
				return got.Username
			}
			return ""
		}(), err)
	}

	form := url.Values{"username": {"alberto"}, "password": {"correct-horse"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.0.1:7777"
	lw := httptest.NewRecorder()
	s.handleLoginSubmit(lw, req)
	if lw.Code != http.StatusSeeOther {
		t.Fatalf("login as renamed user = %d, want 303; body: %s", lw.Code, lw.Body.String())
	}
	if loc := lw.Header().Get("Location"); loc != "/apps" {
		t.Errorf("redirect = %q, want /apps", loc)
	}
}
