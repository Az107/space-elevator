package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/store"
)

func TestCSRFKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCSRFKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Same key must reload from disk (restart survival).
	c2, err := loadCSRFKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok := c.Token("session-1")
	if !c2.Verify("session-1", tok) {
		t.Error("token from a reloaded key must verify")
	}
	if c.Verify("session-2", tok) {
		t.Error("token must be bound to the session ID")
	}
	if c.Verify("session-1", "") || c.Verify("session-1", tok+"x") {
		t.Error("empty or tampered tokens must not verify")
	}
}

func TestCSRFProtect(t *testing.T) {
	c, err := loadCSRFKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{CSRF: c}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := s.csrfProtect(next)

	withSession := func(req *http.Request) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), ctxSession, &store.Session{ID: "sess-123"}))
	}
	formReq := func(body string, hdr map[string]string) *http.Request {
		req := httptest.NewRequest("POST", "/apps/x/remove", strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return withSession(req)
	}

	// Missing session → 403
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/x", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("no session: got %d want 403", rr.Code)
	}

	// POST without token → 403
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, formReq("", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("missing token: got %d want 403", rr.Code)
	}

	// POST with form token → 200
	body := "csrf=" + c.Token("sess-123")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, formReq(body, nil))
	if rr.Code != 200 {
		t.Errorf("valid form token: got %d want 200", rr.Code)
	}

	// POST with header token → 200 (multipart path)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, formReq("", map[string]string{"X-CSRF-Token": c.Token("sess-123")}))
	if rr.Code != 200 {
		t.Errorf("valid header token: got %d want 200", rr.Code)
	}

	// Wrong token → 403
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, formReq("csrf=deadbeef", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("invalid token: got %d want 403", rr.Code)
	}

	// GET is exempt
	req := withSession(httptest.NewRequest("GET", "/x", nil))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("GET without token: got %d want 200", rr.Code)
	}
}

func TestOriginCheck(t *testing.T) {
	s := &Server{Cfg: &config.Config{PublicHost: "elevator.example.com"}}
	h := s.originCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	req := func(origin, fetchSite, host string) *http.Request {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		return r
	}

	cases := []struct {
		name      string
		origin    string
		fetchSite string
		host      string
		want      int
	}{
		{"no headers (curl)", "", "", "elevator.example.com", 200},
		{"same origin+host", "https://elevator.example.com", "same-origin", "elevator.example.com", 200},
		// Safari/Chrome quirk: Origin: null on a form POST navigation,
		// with truthful Fetch Metadata — must be accepted.
		{"null origin, sec-fetch same-origin", "null", "same-origin", "elevator.example.com", 200},
		{"null origin, sec-fetch same-site", "null", "same-site", "host.containers.internal:8080", 200},
		{"same-origin, proxy-rewritten host", "https://elevator.example.com", "same-origin", "host.containers.internal:8080", 200},
		{"cross-site attested", "https://evil.albruiz.dev", "cross-site", "elevator.example.com", 403},
		{"cross-site, null origin", "null", "cross-site", "elevator.example.com", 403},
		// Legacy clients (no Sec-Fetch-Site).
		{"legacy: evil sibling", "https://evil.albruiz.dev", "", "elevator.example.com", 403},
		{"legacy: suffix trick", "https://elevator.example.com.evil.com", "", "elevator.example.com", 403},
		{"legacy: sandboxed null", "null", "", "elevator.example.com", 403},
		{"legacy: public-host match", "https://elevator.example.com", "", "host.containers.internal:8080", 200},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req(tc.origin, tc.fetchSite, tc.host))
		if rr.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, rr.Code, tc.want)
		}
	}

	// GETs are never blocked
	r := req("https://evil.example.com", "cross-site", "elevator.example.com")
	r.Method = http.MethodGet
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Errorf("GET with cross-site: got %d want 200", rr.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/x", nil))
	for _, name := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"} {
		if rr.Header().Get(name) == "" {
			t.Errorf("missing header %s", name)
		}
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Error("CSP must keep frame-ancestors 'self' for the files iframe")
	}
}

func TestValidationHelpers(t *testing.T) {
	goodNames := []string{"a", "web-1", "my-app-42", "drop-12345678"}
	for _, n := range goodNames {
		if !validAppName(n) {
			t.Errorf("name %q should be valid", n)
		}
	}
	badNames := []string{"", "-lead", "UPPER", "has space", "../etc", "a/b", "ends-", strings.Repeat("x", 64)}
	for _, n := range badNames {
		if validAppName(n) {
			t.Errorf("name %q should be invalid", n)
		}
	}

	goodDomains := []string{"app.example.com", "a.io", "my-app.example.co.uk", "x.y.z.deep.example.io"}
	for _, d := range goodDomains {
		if !validDomain(d) {
			t.Errorf("domain %q should be valid", d)
		}
	}
	badDomains := []string{
		"", "example", "-.example.com", "under_score.example.com", "a b.com",
		"evil`) || Host(`x", "*.example.com", "example.com/", "ex..ample.com",
		strings.Repeat("a", 250) + ".com",
	}
	for _, d := range badDomains {
		if validDomain(d) {
			t.Errorf("domain %q should be invalid", d)
		}
	}
}
