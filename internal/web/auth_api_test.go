package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/tokenmanager"
)

func apiAuthServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	tm, err := tokenmanager.New(ts.URL, "app_test", "cs_test")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{TokenManager: tm}
}

func hitAPI(s *Server, rawToken string) int {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := s.requireAPIToken(next)
	req := httptest.NewRequest("GET", "/api/v1/apps", nil)
	if rawToken != "" {
		req.Header.Set("Authorization", "Bearer "+rawToken)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func validTokenHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token != "tm_test_secret" {
		w.Header().Set("X-Error", "invalid_token")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("X-Token-Id", "tok_1")
	w.Header().Set("X-Token-Name", "ci")
	w.Header().Set("X-Token-App", "space-elevator")
	w.Header().Set("X-Token-Expires-At", "never")
	w.WriteHeader(http.StatusNoContent)
}

func TestRequireAPIToken(t *testing.T) {
	s := apiAuthServer(t, validTokenHandler)

	// No header → 401 with a challenge.
	if code := hitAPI(s, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", code)
	}
	// Garbage and legacy tokens are rejected by Token-Manager.
	if code := hitAPI(s, "tm_garbage"); code != http.StatusUnauthorized {
		t.Errorf("garbage token: %d, want 401", code)
	}
	if code := hitAPI(s, "se_legacy"); code != http.StatusUnauthorized {
		t.Errorf("legacy token: %d, want 401", code)
	}
	// A valid token passes through the middleware.
	if code := hitAPI(s, "tm_test_secret"); code != http.StatusTeapot {
		t.Errorf("valid token: %d, want %d", code, http.StatusTeapot)
	}
}

func TestRequireAPITokenManagerUnavailable(t *testing.T) {
	s := &Server{}
	if code := hitAPI(s, "tm_any"); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured manager: %d, want 503", code)
	}

	s = apiAuthServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	if code := hitAPI(s, "tm_any"); code != http.StatusServiceUnavailable {
		t.Errorf("unavailable manager: %d, want 503", code)
	}
}

func TestRequireAPITokenActorMetadata(t *testing.T) {
	s := apiAuthServer(t, validTokenHandler)
	var got audit.Actor
	h := s.requireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = audit.ActorFromCtx(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest("GET", "/api/v1/apps", nil)
	req.Header.Set("Authorization", "Bearer tm_test_secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if got.Type != "token" || got.ID != "tok_1" || got.Label != "ci" {
		t.Errorf("actor = %+v, want token/tok_1/ci", got)
	}
}

func TestBearerTokenParsing(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if bearerToken(req) != "" {
		t.Error("no header must yield empty token")
	}
	req.Header.Set("Authorization", "Basic abc")
	if bearerToken(req) != "" {
		t.Error("non-bearer scheme must yield empty token")
	}
	req.Header.Set("Authorization", "Bearer tm_abc")
	if bearerToken(req) != "tm_abc" {
		t.Errorf("got %q", bearerToken(req))
	}
}
