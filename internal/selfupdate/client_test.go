package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRepo(t *testing.T) {
	cases := []struct {
		in                string
		owner, repo, host string
		wantErr           bool
	}{
		{"https://github.com/Az107/space-elevator", "Az107", "space-elevator", "github.com", false},
		{"https://github.com/Az107/space-elevator.git", "Az107", "space-elevator", "github.com", false},
		{"git@github.com:Az107/space-elevator.git", "Az107", "space-elevator", "github.com", false},
		{"Az107/space-elevator", "Az107", "space-elevator", "github.com", false},
		{"", "", "", "", true},
		{"https://gitlab.com/o/r", "o", "r", "gitlab.com", false},
		{"just-a-name", "", "", "", true},
	}
	for _, c := range cases {
		owner, repo, host, err := ParseRepo(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRepo(%q) = nil error, want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepo(%q) error: %v", c.in, err)
			continue
		}
		if owner != c.owner || repo != c.repo || host != c.host {
			t.Errorf("ParseRepo(%q) = (%q,%q,%q), want (%q,%q,%q)", c.in, owner, repo, host, c.owner, c.repo, c.host)
		}
	}
}

func TestNewClientRejectsNonGitHub(t *testing.T) {
	if _, err := NewClient("https://gitlab.com/o/r", ""); !errors.Is(err, ErrUnsupportedHost) {
		t.Fatalf("NewClient(gitlab) error = %v, want ErrUnsupportedHost", err)
	}
}

func TestParseChecksums(t *testing.T) {
	body := "abc123\n" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  space-elevator-linux-amd64\n" +
		"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210 *space-elevator-linux-arm64\n"
	got, err := ParseChecksums(body, "space-elevator-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210" {
		t.Fatalf("ParseChecksums = %q", got)
	}
	if _, err := ParseChecksums(body, "space-elevator-linux-386"); err == nil {
		t.Fatal("ParseChecksums for missing asset = nil error, want error")
	}
}

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient("Az107/space-elevator", "")
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = srv.URL
	c.GOOS, c.GOARCH = "linux", "arm64"
	return c, srv
}

func TestLatestAndExpectedSHA(t *testing.T) {
	bin := []byte("fake-binary-bytes")
	const sum = "0000000000000000000000000000000000000000000000000000000000000000"
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/Az107/space-elevator/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v0.2.0","draft":false,"assets":[
			{"id":1,"name":"space-elevator-linux-arm64","size":` + itoa(len(bin)) + `,"url":"` + srv.URL + `/asset/bin"},
			{"id":2,"name":"checksums.txt","size":80,"url":"` + srv.URL + `/asset/cs"}
		]}`))
	})
	mux.HandleFunc("/asset/cs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sum + "  space-elevator-linux-arm64\n"))
	})
	mux.HandleFunc("/asset/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bin)
	})
	c, s := newTestClient(t, mux)
	srv = s

	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.2.0" {
		t.Fatalf("TagName = %q", rel.TagName)
	}
	asset, err := FindAsset(rel, AssetName(c.GOOS, c.GOARCH))
	if err != nil {
		t.Fatal(err)
	}
	sha, err := c.ExpectedSHA(context.Background(), rel, asset.Name)
	if err != nil {
		t.Fatal(err)
	}
	if sha != sum {
		t.Fatalf("ExpectedSHA = %q, want %q", sha, sum)
	}
	var buf bytes.Buffer
	n, err := c.DownloadAsset(context.Background(), asset, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(bin)) || !bytes.Equal(buf.Bytes(), bin) {
		t.Fatalf("DownloadAsset = %d bytes %q", n, buf.Bytes())
	}
}

func TestLatestNoRelease(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if _, err := c.Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("Latest = %v, want ErrNoRelease", err)
	}
}

func TestLatestRateLimited(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	if _, err := c.Latest(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Latest = %v, want ErrRateLimited", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
