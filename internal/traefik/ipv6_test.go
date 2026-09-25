package traefik

import (
	"net/url"
	"strings"
	"testing"
)

// TestRenderIPv6BackendIsValidURL is the regression guard for the IPv6
// backend bug: the old fmt.Sprintf("http://%s:%d") produced
// "http://fd00::2:8080", which is not a parseable URL, so Traefik failed to
// load the file and the app 404'd with no obvious cause.
func TestRenderIPv6BackendIsValidURL(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "app", Name: "web", Domain: []string{"app.example.com"}, IP: "fd00::2", Port: 8080},
		},
		PublicHost:    "elevator.example.com",
		AppPathPrefix: "/app/",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "http://[fd00::2]:8080") {
		t.Fatalf("IPv6 backend not bracketed:\n%s", body)
	}
	// The emitted server URL must actually parse.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- url:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "- url:"))
		raw = strings.Trim(raw, `"'`)
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("emitted server URL %q does not parse: %v", raw, err)
		}
		if u.Hostname() != "fd00::2" {
			t.Fatalf("hostname round-trip failed: %q", u.Hostname())
		}
	}
}

// TestRenderIPv4BackendUnchanged guards against the fix bracketing IPv4 too.
func TestRenderIPv4BackendUnchanged(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes:        []ServiceRoute{{AppName: "app", Name: "web", Domain: []string{"a.example.com"}, IP: "10.89.0.5", Port: 80}},
		PublicHost:    "elevator.example.com",
		AppPathPrefix: "/app/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "http://10.89.0.5:80") {
		t.Fatalf("IPv4 backend changed:\n%s", got)
	}
}
