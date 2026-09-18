package web

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/store"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		cf     string
		xff    string
		xreal  string
		want   string
	}{
		{"direct public peer", "203.0.113.5:1234", "", "", "", "203.0.113.5"},
		{"cloudflare through trusted proxy", "10.0.0.1:1234", "203.0.113.9", "", "", "203.0.113.9"},
		{"xff through trusted proxy", "127.0.0.1:1234", "", "203.0.113.9, 10.0.0.1", "", "203.0.113.9"},
		{"x-real-ip through trusted proxy", "192.168.1.1:1", "", "", "198.51.100.3", "198.51.100.3"},
		{"spoofed headers from public peer ignored", "203.0.113.5:1234", "1.2.3.4", "1.2.3.4", "1.2.3.4", "203.0.113.5"},
		{"invalid cf falls through to xff", "10.0.0.1:1", "not-an-ip", "198.51.100.2", "", "198.51.100.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.cf != "" {
				r.Header.Set("CF-Connecting-IP", tc.cf)
			}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xreal != "" {
				r.Header.Set("X-Real-IP", tc.xreal)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func loginTestServer(t *testing.T) (*Server, *store.Store) {
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
	return &Server{
		Store:    st,
		Renderer: r,
		Audit:    audit.New(st, &bytes.Buffer{}),
		logins:   newLoginLimiter(),
		Cfg:      &config.Config{},
	}, st
}

func postLogin(t *testing.T, s *Server, user, pass, remote string) {
	t.Helper()
	form := url.Values{"username": {user}, "password": {pass}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote
	s.handleLoginSubmit(httptest.NewRecorder(), req)
}

func TestLoginAudit(t *testing.T) {
	s, st := loginTestServer(t)
	ctx := t.Context()

	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{ID: "u1", Username: "admin", PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}

	postLogin(t, s, "admin", "wrong", "203.0.113.5:1111")
	postLogin(t, s, "ghost", "whatever", "203.0.113.6:2222")
	postLogin(t, s, "admin", "correct-horse", "203.0.113.5:1111")

	events, err := st.ListAudit(ctx, store.AuditFilter{Action: audit.ActionLogin})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("login events = %d, want 3", len(events))
	}

	byOutcome := map[string]int{}
	for _, e := range events {
		byOutcome[e.Outcome]++
		if e.IP == "" {
			t.Errorf("event missing client IP: %+v", e)
		}
	}
	if byOutcome[audit.OutcomeFailure] != 2 || byOutcome[audit.OutcomeSuccess] != 1 {
		t.Errorf("outcomes = %v, want 2 failure / 1 success", byOutcome)
	}
}

func TestRecordAuditNilLoggerSafe(t *testing.T) {
	// Servers built in tests without an Audit logger must not panic when
	// handlers record events (e.g. the app detail test harness).
	s := &Server{}
	req := httptest.NewRequest("POST", "/x", nil)
	s.recordAudit(req, audit.ActionAppRemove, "app", "a1", "web", audit.OutcomeSuccess, "")
}
