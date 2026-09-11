package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mustUser creates a user so session FKs resolve.
func mustUser(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.CreateUser(t.Context(), &User{ID: id, Username: id, PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTokenHashing(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUser(t, s, "u1")

	sess := &Session{ID: "raw-cookie-token", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	// The raw token resolves…
	got, err := s.GetSession(ctx, "raw-cookie-token")
	if err != nil {
		t.Fatalf("raw token lookup failed: %v", err)
	}
	if got.ID != "raw-cookie-token" || got.UserID != "u1" {
		t.Errorf("unexpected session %+v", got)
	}

	// …the hashed form must never be usable as a cookie…
	if _, err := s.GetSession(ctx, hashSessionToken("raw-cookie-token")); err == nil {
		t.Error("hash must not be accepted as a raw token")
	}

	// …and the DB row must not contain the raw token.
	var id string
	if err := s.db.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == "raw-cookie-token" || id == "" {
		t.Errorf("session id stored plaintext: %q", id)
	}
	if _, err := s.GetSession(ctx, "wrong-token"); err == nil {
		t.Error("unknown token must not resolve")
	}
}

func TestTouchAndPurgeSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUser(t, s, "u1")

	sess := &Session{ID: "tok", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	earlier := time.Now().Add(-2 * time.Hour)
	if err := s.TouchSession(ctx, "tok", earlier); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(ctx, "tok")
	if !got.LastSeenAt.Equal(earlier.Truncate(time.Second)) {
		t.Errorf("last_seen = %v, want %v", got.LastSeenAt, earlier.Truncate(time.Second))
	}

	// Expired sessions are purged; live ones survive.
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at=? WHERE id=?`,
		time.Now().Add(-time.Hour).Unix(), hashSessionToken("tok")); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "tok"); err == nil {
		t.Error("expired session should have been purged")
	}
}

func TestDeleteSessionsForUser(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUser(t, s, "u1")
	mustUser(t, s, "u2")
	for _, tok := range []string{"a", "b", "c"} {
		if err := s.CreateSession(ctx, &Session{ID: tok, UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, &Session{ID: "other", UserID: "u2", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSessionsForUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"a", "b", "c"} {
		if _, err := s.GetSession(ctx, tok); err == nil {
			t.Errorf("session %q should be gone", tok)
		}
	}
	if _, err := s.GetSession(ctx, "other"); err != nil {
		t.Error("other user's session must survive")
	}
}

func TestUpdateUserPassword(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	if err := s.CreateUser(ctx, &User{ID: "u1", Username: "admin", PasswordHash: "old-hash"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUserPassword(ctx, "u1", "new-hash"); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash != "new-hash" {
		t.Errorf("password hash = %q, want new-hash", u.PasswordHash)
	}
	if err := s.UpdateUserPassword(ctx, "missing", "x"); err == nil {
		t.Error("update of unknown user must fail")
	}
}

func TestUpdateUsername(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	if err := s.CreateUser(ctx, &User{ID: "u1", Username: "admin", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{ID: "u2", Username: "ops", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUsername(ctx, "u1", "albert"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUserByID(ctx, "u1")
	if u.Username != "albert" {
		t.Errorf("username = %q, want albert", u.Username)
	}
	if err := s.UpdateUsername(ctx, "u1", "ops"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("collision err = %v, want ErrUsernameTaken", err)
	}
	if err := s.UpdateUsername(ctx, "missing", "x"); err == nil {
		t.Error("rename of unknown user must fail")
	}
}

func TestDeleteOtherSessionsForUser(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	mustUser(t, s, "u1")
	for _, tok := range []string{"keep", "a", "b"} {
		if err := s.CreateSession(ctx, &Session{ID: tok, UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteOtherSessionsForUser(ctx, "u1", "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "keep"); err != nil {
		t.Error("kept session must survive")
	}
	for _, tok := range []string{"a", "b"} {
		if _, err := s.GetSession(ctx, tok); err == nil {
			t.Errorf("session %q should be gone", tok)
		}
	}
}

func TestAppLastError(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	app := &App{ID: "id1", Name: "web", SourceType: "drop", ComposeYAML: "services: {}", Status: "pending", LastError: ""}
	if err := s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppStatusErr(ctx, "id1", "error", "git clone failed: boom"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAppByName(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "error" || got.LastError != "git clone failed: boom" {
		t.Errorf("status=%q lastError=%q", got.Status, got.LastError)
	}
	if err := s.UpdateAppStatusErr(ctx, "id1", "running", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAppByName(ctx, "web")
	if got.LastError != "" {
		t.Errorf("error should be cleared, got %q", got.LastError)
	}
}

func TestDatabaseFilePermissions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	info, err := os.Stat(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("db file mode = %o, want 600", perm)
	}
}

// The runtime slug defaults to the app name and must not change when the
// app is renamed, so containers/routes/images keep resolving.
func TestAppSlugDefaultsAndSurvivesRename(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	a := &App{ID: "a1", Name: "my-api", SourceType: "git", Status: "running", Env: map[string]string{}}
	if err := s.CreateApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.Slug != "my-api" {
		t.Fatalf("slug = %q, want my-api", a.Slug)
	}

	if err := s.UpdateAppName(ctx, "a1", "renamed-api"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApp(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed-api" {
		t.Errorf("name = %q, want renamed-api", got.Name)
	}
	if got.Slug != "my-api" {
		t.Errorf("slug changed on rename: %q, want my-api", got.Slug)
	}
	if byNew, err := s.GetAppByName(ctx, "renamed-api"); err != nil || byNew.ID != "a1" {
		t.Fatalf("lookup by new name failed: %v", err)
	}
	if _, err := s.GetAppByName(ctx, "my-api"); err == nil {
		t.Error("old name should no longer resolve")
	}
}
