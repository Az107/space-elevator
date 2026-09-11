package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/albertoruiz/space-elevator/internal/store"
)

// apiAuthStore builds a middleware-backed handler against a fresh
// store, returning what's needed to exercise token auth end to end.
func apiAuthStore(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{Store: st}
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

func TestRequireAPIToken(t *testing.T) {
	s := apiAuthStore(t)

	// No header → 401 with a challenge.
	if code := hitAPI(s, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", code)
	}
	// Garbage token → 401.
	if code := hitAPI(s, "se_garbage"); code != http.StatusUnauthorized {
		t.Errorf("garbage token: %d, want 401", code)
	}
	// Token without the se_ marker → 401 (rejected before hashing).
	if code := hitAPI(s, "plainvalue"); code != http.StatusUnauthorized {
		t.Errorf("unprefixed token: %d, want 401", code)
	}
	// Valid token passes…
	_, raw, err := s.Store.MintAPIToken(t.Context(), "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := hitAPI(s, raw); code != http.StatusTeapot {
		t.Errorf("valid token: %d, want %d", code, http.StatusTeapot)
	}
	// …and the raw token keeps working (touch recorded).
	toks, _ := s.Store.ListAPITokens(t.Context())
	if toks[0].LastUsedAt == nil {
		t.Error("last_used_at should be recorded on first use")
	}
}

func TestRequireAPITokenExpired(t *testing.T) {
	s := apiAuthStore(t)
	past := time.Now().Add(-time.Hour)
	_, raw, err := s.Store.MintAPIToken(t.Context(), "old", &past)
	if err != nil {
		t.Fatal(err)
	}
	if code := hitAPI(s, raw); code != http.StatusUnauthorized {
		t.Errorf("expired token: %d, want 401", code)
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
	req.Header.Set("Authorization", "Bearer se_abc")
	if bearerToken(req) != "se_abc" {
		t.Errorf("got %q", bearerToken(req))
	}
}
