package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// TestRenderAllPages is a smoke test: every template must execute without
// error for representative data. When SE_RENDER_OUT is set, the full HTML
// of each page is written there (used for visual previews).
func TestRenderAllPages(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("SE_RENDER_OUT")

	render := func(name string, w *httptest.ResponseRecorder, page string, data any) {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		r.Render(w, req, page, data)
		if w.Code != 200 {
			t.Errorf("%s: status %d", name, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "<%") || strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s: suspicious template output", name)
		}
		if out != "" {
			_ = os.MkdirAll(out, 0o755)
			_ = os.WriteFile(filepath.Join(out, name+".html"), []byte(body), 0o644)
		}
	}

	now := time.Date(2026, 9, 7, 14, 3, 0, 0, time.UTC)
	apps := []*store.App{
		{ID: "11111111", Name: "atlas-api", SourceType: "git", SourceRef: "https://github.com/you/atlas-api.git", Status: "running", CreatedAt: now},
		{ID: "22222222", Name: "docs-site", SourceType: "drop", SourceRef: "portfolio-v3.zip", Status: "pending", CreatedAt: now.Add(-24 * time.Hour)},
		{ID: "33333333", Name: "webhooks", SourceType: "git", SourceRef: "https://github.com/you/webhooks.git", Status: "error", CreatedAt: now.Add(-72 * time.Hour)},
	}
	rr := httptest.NewRecorder()
	render("apps", rr, "apps.html", appsListData{
		PageData:     PageData{Authed: true, Title: "Apps"},
		Apps:         apps,
		DomainsByApp: map[string][]string{"11111111": {"api.elevator.dev"}},
	})

	rr = httptest.NewRecorder()
	render("app_detail", rr, "app_detail.html", appDetailData{
		PageData: PageData{Authed: true, Title: "atlas-api"},
		App:      apps[0],
		Status:   "running",
		Services: []string{"api", "worker"},
		Domains:  []string{"api.elevator.dev"},
	})

	rr = httptest.NewRecorder()
	render("app_detail_empty", rr, "app_detail.html", appDetailData{
		PageData: PageData{Authed: true, Title: "docs-site"},
		App:      apps[1],
		Status:   "pending",
	})

	rr = httptest.NewRecorder()
	render("deploy", rr, "deploy.html", deployFormData{PageData: PageData{Authed: true, Title: "Deploy"}})

	rr = httptest.NewRecorder()
	render("settings", rr, "settings.html", settingsData{
		PageData:               PageData{Authed: true, Title: "Settings"},
		SocketPath:             "/run/user/1000/podman/podman.sock",
		TraefikDir:             "/home/you/Infra/traefik/rootful-dynamic",
		CertResolver:           "letsencrypt",
		PublicHost:             "elevator.albruiz.dev",
		AppPathPrefix:          "/app/",
		SelfRouteExists:        false,
		SelfRouteURL:           "https://elevator.albruiz.dev/dashboard/",
		TokenManagerURL:        "http://127.0.0.1:8000",
		TokenManagerConfigured: true,
		Creds:                  []*store.GitCredential{{Host: "github.com", Username: "x-access-token"}},
	})
	if body := rr.Body.String(); !strings.Contains(body, "https://elevator.albruiz.dev/app/") {
		t.Errorf("settings page did not render a valid public app URL")
	}

	rr = httptest.NewRecorder()
	render("login", rr, "login.html", authData{PageData: PageData{Title: "Sign in"}})
	rr = httptest.NewRecorder()
	render("setup", rr, "setup.html", authData{PageData: PageData{Title: "Welcome"}})

	// files fragment (rendered standalone for the iframe)
	frag := &filesFragmentData{
		App: apps[0],
		Tree: &fileNode{Name: ".", IsDir: true, Children: []*fileNode{
			{Name: "Dockerfile", Path: "Dockerfile", Size: 412},
			{Name: "src", Path: "src", IsDir: true, Children: []*fileNode{
				{Name: "main.go", Path: "src/main.go", Size: 1337},
			}},
		}},
	}
	rr = httptest.NewRecorder()
	r.RenderFragment(rr, "files.html", frag)
	if rr.Code != 200 {
		t.Errorf("files fragment: status %d", rr.Code)
	}
	if out != "" {
		_ = os.WriteFile(filepath.Join(out, "files.html"), rr.Body.Bytes(), 0o644)
	}
}

// The dashboard shares the Token-Manager interaction language: useful
// labelled controls, staged rows, and a visible current-nav cue. Keep
// these in the render smoke coverage so a visual refactor cannot quietly
// remove them from a page template.
func TestSharedDesignSystem(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.Render(w, httptest.NewRequest("GET", "/apps", nil), "apps.html", appsListData{
		PageData:     PageData{Authed: true, Title: "Apps"},
		Apps:         []*store.App{{ID: "11111111", Name: "atlas-api", SourceType: "git", Status: "running", CreatedAt: time.Now()}},
		DomainsByApp: map[string][]string{"11111111": {"api.example.com"}},
	})
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`style="--row-index: 0"`,
		`data-nav="apps"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered apps page missing %q", want)
		}
	}
	empty := httptest.NewRecorder()
	r.Render(empty, httptest.NewRequest("GET", "/apps", nil), "apps.html", appsListData{
		PageData: PageData{Authed: true, Title: "Apps"},
	})
	if !strings.Contains(empty.Body.String(), `class="orbit"`) {
		t.Error("empty apps page missing the animated orbit artwork")
	}

	css, err := content.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".topnav a::after",
		"animation: row-in .22s ease-out both",
		"animation: modal-in .16s ease-out",
		"prefers-reduced-motion: reduce",
		"translateY(-1px) scale(1.02)",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("shared design CSS missing %q", want)
		}
	}

	js, err := content.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "setAttribute('aria-current', 'page')") {
		t.Error("nav script does not expose the current page to assistive technology")
	}
}

// TestPageFormsCarryCSRFToken guards against a regression where page
// templates rendered value="" into every hidden csrf input while only
// the layout nav (via $.CSRFToken) got the real token.
func TestPageFormsCarryCSRFToken(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	const tok = "deadbeef"
	req := httptest.NewRequest("GET", "/settings", nil)
	ctx := context.WithValue(req.Context(), ctxCSRF, tok)
	req = req.WithContext(ctx)

	pages := []struct {
		page string
		data any
	}{
		{"settings.html", settingsData{PageData: pageCtx(req, "Settings")}},
		{"deploy.html", deployFormData{PageData: pageCtx(req, "Deploy")}},
		{"apps.html", appsListData{PageData: pageCtx(req, "Apps")}},
	}
	for _, p := range pages {
		w := httptest.NewRecorder()
		r.Render(w, req, p.page, p.data)
		if w.Code != 200 {
			t.Errorf("%s: status %d", p.page, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `value="deadbeef"`) {
			t.Errorf("%s: no csrf token in rendered forms", p.page)
		}
		if strings.Contains(body, `name="csrf" value=""`) {
			t.Errorf("%s: empty csrf token present", p.page)
		}
	}
}
