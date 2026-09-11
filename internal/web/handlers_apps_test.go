package web

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/store"
)

func newDetailTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		Store:     st,
		Renderer:  r,
		BuildLogs: newBuildLogRegistry(),
		Cfg:       &config.Config{AppsRoot: t.TempDir()},
	}
}

func mustApp(t *testing.T, s *Server, a *store.App) {
	t.Helper()
	if err := s.Store.CreateApp(t.Context(), a); err != nil {
		t.Fatal(err)
	}
}

// The redirect from the git deploy form lands on the app detail page
// while the clone is still running and ComposeYAML is still empty.
// That state must render the pending view — never a 500 carrying the
// "compose file has no services" parse error.
func TestHandleAppDetailEmptyComposeRendersPending(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{
		ID:         "11111111",
		Name:       "atlas-api",
		SourceType: "git",
		SourceRef:  "https://github.com/you/atlas-api.git",
		Status:     "pending",
		Env:        map[string]string{},
	})

	r := chi.NewRouter()
	r.Get("/apps/{name}", s.handleAppDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/atlas-api", nil))
	if w.Code != 200 {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "lamp-pending") {
		t.Error("page must show the pending lamp")
	}
	if strings.Contains(body, "compose file has no services") {
		t.Error("compose parse error must not leak into the page")
	}
	if !strings.Contains(body, `id="logs-panel"`) || !strings.Contains(body, "logs-panel deploying") {
		t.Error("deploying page must show the logs panel in its deploying state")
	}
}

// A git deploy that failed before compose persistence (bad clone, no
// compose file in the repo) renders the row's error state, not a 500.
func TestHandleAppDetailFailedDeployRendersError(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{
		ID:         "22222222",
		Name:       "webhooks",
		SourceType: "git",
		SourceRef:  "https://github.com/you/webhooks.git",
		Status:     "error",
		LastError:  "git clone: authentication failed",
		Env:        map[string]string{},
	})

	r := chi.NewRouter()
	r.Get("/apps/{name}", s.handleAppDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/webhooks", nil))
	if w.Code != 200 {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "authentication failed") {
		t.Error("page must surface the stored error")
	}
	if !strings.Contains(body, "lamp-error") {
		t.Error("page must show the error lamp")
	}
}

// The deploy-status feed returns the row status plus build-log lines
// after the client's last seen seq.
func TestHandleDeployStatus(t *testing.T) {
	s := newDetailTestServer(t)
	mustApp(t, s, &store.App{
		ID:         "33333333",
		Name:       "docs-site",
		SourceType: "git",
		Status:     "pending",
		Env:        map[string]string{},
	})
	s.BuildLogs.Append("docs-site", "Cloning https://example.com/repo.git (main)...")
	s.BuildLogs.Append("docs-site", "building image localhost/se/docs-site/web:latest...")

	r := chi.NewRouter()
	r.Get("/apps/{name}/deploy-status", s.handleDeployStatus)

	fetch := func(query string) (int, string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/docs-site/deploy-status"+query, nil))
		return w.Code, w.Body.String()
	}

	code, body := fetch("?after=0")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{`"status":"pending"`, `"seq":2`, "Cloning", "building image"} {
		if !strings.Contains(body, want) {
			t.Errorf("full feed missing %q in %s", want, body)
		}
	}

	code, body = fetch("?after=1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if strings.Contains(body, "Cloning") {
		t.Error("incremental feed re-served an already seen line")
	}
	if !strings.Contains(body, "building image") {
		t.Errorf("incremental feed missing the new line: %s", body)
	}
}
