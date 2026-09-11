package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// Renaming changes the display name and URL but must leave the runtime
// slug (container/route/image identity) untouched.
func TestHandleAppRename(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "r1", Name: "old-name", SourceType: "git", Status: "running", Env: map[string]string{}})

	r := chi.NewRouter()
	r.Post("/apps/{name}/rename", s.handleAppRename)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/old-name/rename", strings.NewReader("name=new-name"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/apps/new-name" {
		t.Fatalf("redirect = %q, want /apps/new-name", loc)
	}
	got, err := s.Store.GetAppByName(t.Context(), "new-name")
	if err != nil {
		t.Fatalf("new name not found: %v", err)
	}
	if got.Slug != "old-name" {
		t.Errorf("slug = %q, want old-name (runtime identity must not change)", got.Slug)
	}
}

func TestHandleAppRenameRejectsDuplicate(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "r1", Name: "one", SourceType: "git", Status: "running", Env: map[string]string{}})
	mustApp(t, s, &store.App{ID: "r2", Name: "two", SourceType: "git", Status: "running", Env: map[string]string{}})

	r := chi.NewRouter()
	r.Post("/apps/{name}/rename", s.handleAppRename)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/one/rename", strings.NewReader("name=two"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", w.Code)
	}
	// Redirects back to the original page, not the taken name.
	if loc := w.Header().Get("Location"); loc != "/apps/one" {
		t.Fatalf("redirect = %q, want /apps/one", loc)
	}
	if _, err := s.Store.GetAppByName(t.Context(), "one"); err != nil {
		t.Errorf("original app should still exist: %v", err)
	}
}

func TestHandleAppRenameRejectsInvalid(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "r1", Name: "one", SourceType: "git", Status: "running", Env: map[string]string{}})

	r := chi.NewRouter()
	r.Post("/apps/{name}/rename", s.handleAppRename)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/apps/one/rename", strings.NewReader("name=Not Valid!"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if loc := w.Header().Get("Location"); loc != "/apps/one" {
		t.Fatalf("redirect = %q, want /apps/one", loc)
	}
	if got, _ := s.Store.GetAppByName(t.Context(), "one"); got == nil || got.Name != "one" {
		t.Error("app name must be unchanged after rejected rename")
	}
}

// A flash cookie set by a failed form POST is consumed into the page and
// cleared, and the layout renders it as dismissible toast markup.
func TestFlashRenderedAndCleared(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "f1", Name: "atlas", SourceType: "git", Status: "error", Env: map[string]string{}})

	r := chi.NewRouter()
	r.Get("/apps/{name}", s.flashMessages(http.HandlerFunc(s.handleAppDetail)).ServeHTTP)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/apps/atlas", nil)
	req.AddCookie(&http.Cookie{
		Name:  flashCookie,
		Value: url.QueryEscape(encodeFlash(flashError, "domain already attached to another app")),
	})
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "data-flash") {
		t.Error("toast markup missing")
	}
	if !strings.Contains(body, "domain already attached to another app") {
		t.Error("flash text missing from page")
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == flashCookie && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("flash cookie should be cleared after rendering")
	}
}

// A stopped app offers Start (and the rename modal is present on the
// detail page). Empty compose keeps the row's own status authoritative.
func TestAppDetailStoppedShowsStartAndRename(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{ID: "s1", Name: "stopped-app", SourceType: "git", Status: "stopped", Env: map[string]string{}})

	r := chi.NewRouter()
	r.Get("/apps/{name}", s.handleAppDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/stopped-app", nil))

	body := w.Body.String()
	if !strings.Contains(body, "/apps/stopped-app/start") {
		t.Error("stopped app should offer a Start action")
	}
	if !strings.Contains(body, "rename-dialog") {
		t.Error("rename dialog missing from detail page")
	}
}
