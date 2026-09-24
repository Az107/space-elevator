package tokenmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c, err := New(ts.URL, "app_test", "cs_test")
	if err != nil {
		t.Fatal(err)
	}
	return c, ts
}

func TestValidate(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/validate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Client-Id") != "app_test" || r.Header.Get("X-Client-Secret") != "cs_test" {
			t.Error("client credentials were not sent")
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(r, &body); err != nil || body.Token != "tm_test_secret" {
			t.Errorf("request body = %+v, err = %v", body, err)
		}
		w.Header().Set("X-Token-Id", "tok_1")
		w.Header().Set("X-Token-Name", "ci")
		w.Header().Set("X-Token-App", "space-elevator")
		w.Header().Set("X-Token-Expires-At", "never")
		w.WriteHeader(http.StatusNoContent)
	}))

	got, err := c.Validate(context.Background(), "tm_test_secret")
	if err != nil {
		t.Fatal(err)
	}
	want := TokenInfo{ID: "tok_1", Name: "ci", App: "space-elevator", ExpiresAt: "never"}
	if got != want {
		t.Errorf("token info = %+v, want %+v", got, want)
	}
}

func TestValidateInvalidToken(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Error", "invalid_token")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	_, err := c.Validate(context.Background(), "tm_bad")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error = %v, want ErrInvalidToken", err)
	}
}

func TestValidateInvalidClientIsConfigurationError(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Error", "invalid_client")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	_, err := c.Validate(context.Background(), "tm_any")
	if !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("error = %v, want ErrInvalidClient", err)
	}
}

func TestValidateUnavailableResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"rate limit", http.StatusTooManyRequests},
		{"server error", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			_, err := c.Validate(context.Background(), "tm_any")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestValidateRejectsMissingMetadata(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	_, err := c.Validate(context.Background(), "tm_any")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("error = %v, want metadata unavailable error", err)
	}
}

func TestValidateHonorsContext(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Validate(ctx, "tm_any")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		id   string
		sec  string
	}{
		{"empty url", "", "id", "secret"},
		{"bad url", "://bad", "id", "secret"},
		{"missing id", "http://manager", "", "secret"},
		{"missing secret", "http://manager", "id", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.url, tc.id, tc.sec); err == nil {
				t.Fatal("New succeeded unexpectedly")
			}
		})
	}
}

// decodeJSON keeps the test independent of the production request encoder.
func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}
