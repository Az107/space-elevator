package traefik

import (
	"strings"
	"testing"
)

func TestRenderSelf_HostOnly(t *testing.T) {
	got, err := RenderSelf(SelfRouteConfig{
		Host:         "elevator.albruiz.dev",
		BackendURL:   "http://host.containers.internal:8080",
		CertResolver: "letsencrypt",
	})
	if err != nil {
		t.Fatalf("RenderSelf: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"rule: Host(`elevator.albruiz.dev`)",
		"entryPoints:",
		"- websecure",
		"- web",
		"service: space-elevator",
		"certResolver: letsencrypt",
		"redirectScheme:",
		"permanent: true",
		"http://host.containers.internal:8080",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRenderSelf_HostAndPath(t *testing.T) {
	got, err := RenderSelf(SelfRouteConfig{
		Host:         "elevator.albruiz.dev",
		PathPrefix:   "/space-elevator/",
		BackendURL:   "http://host.containers.internal:8080",
		CertResolver: "letsencrypt",
	})
	if err != nil {
		t.Fatalf("RenderSelf: %v", err)
	}
	if !strings.Contains(string(got), "Host(`elevator.albruiz.dev`) && PathPrefix(`/space-elevator/`)") {
		t.Errorf("expected combined host+path rule, got:\n%s", got)
	}
}

func TestRenderSelf_RejectsEmpty(t *testing.T) {
	if _, err := RenderSelf(SelfRouteConfig{BackendURL: "http://x", CertResolver: "l"}); err == nil {
		t.Error("expected error when host and path are both empty")
	}
	if _, err := RenderSelf(SelfRouteConfig{Host: "x", CertResolver: "l"}); err == nil {
		t.Error("expected error when backend URL is empty")
	}
	if _, err := RenderSelf(SelfRouteConfig{Host: "x", BackendURL: "http://x"}); err == nil {
		t.Error("expected error when cert resolver is empty")
	}
}

func TestRender_AppRouteWithSubdomainAndPath(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "web", Name: "frontend", Domain: []string{"myapp.com"}, IP: "10.89.0.5", Port: 80},
		},
		PublicHost:    "elevator.albruiz.dev",
		AppPathPrefix: "/app/",
		CertResolver:  "letsencrypt",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"web-frontend-secure:",
		"Host(`myapp.com`)",
		"web-frontend-web:",
		"web-frontend-path-secure:",
		"Host(`elevator.albruiz.dev`) && PathPrefix(`/app/web-frontend/`)",
		"web-frontend-https:",
		"redirectScheme:",
		"web-frontend-strip:",
		"stripPrefix:",
		"prefixes:",
		"- /app/web-frontend",
		"http://10.89.0.5:80",
		"web-frontend:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRender_AppRouteSubdomainOnly(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "api", Name: "backend", Domain: []string{"api.example.com"}, IP: "10.89.0.6", Port: 3000},
		},
		CertResolver: "letsencrypt",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "api-backend-path-secure") {
		t.Errorf("did not expect path-prefix routers when PublicHost/AppPathPrefix are unset; got:\n%s", s)
	}
	if !strings.Contains(s, "Host(`api.example.com`)") {
		t.Errorf("expected subdomain router, got:\n%s", s)
	}
}

func TestRender_AppRouteBothSubdomainAndPath(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "api", Name: "backend", Domain: []string{"api.example.com"}, IP: "10.89.0.6", Port: 3000},
		},
		PublicHost:    "elevator.albruiz.dev",
		AppPathPrefix: "/app/",
		CertResolver:  "letsencrypt",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"api-backend-secure:",
		"Host(`api.example.com`)",
		"api-backend-path-secure:",
		"Host(`elevator.albruiz.dev`) && PathPrefix(`/app/api-backend/`)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRender_AppRoutePathOnlyWhenNoDomain(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "demo", Name: "web", IP: "10.89.0.7", Port: 8080},
		},
		PublicHost:    "elevator.albruiz.dev",
		AppPathPrefix: "/app/",
		CertResolver:  "letsencrypt",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "Host(`elevator.albruiz.dev`) && PathPrefix(`/app/demo-web/`)") {
		t.Errorf("expected path-prefix route under public host, got:\n%s", s)
	}
	if strings.Contains(s, "demo-web-secure:") {
		t.Errorf("did not expect bare subdomain router when no domain attached; got:\n%s", s)
	}
}

func TestRender_NoRoutesProducesEmpty(t *testing.T) {
	got, err := Render(AppRouteConfig{PublicHost: "x", AppPathPrefix: "/app/", CertResolver: "l"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty body, got:\n%s", got)
	}
}

func TestRender_SkipsRoutesWithoutPortOrIP(t *testing.T) {
	got, err := Render(AppRouteConfig{
		Routes: []ServiceRoute{
			{AppName: "a", Name: "b", IP: "", Port: 80},
			{AppName: "a", Name: "c", IP: "10.0.0.1", Port: 0},
		},
		PublicHost:    "elevator.albruiz.dev",
		AppPathPrefix: "/app/",
		CertResolver:  "letsencrypt",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty body when no route has both IP and Port; got:\n%s", got)
	}
}

func TestSelfRouteConfig_SelfURL(t *testing.T) {
	cases := []struct {
		c    SelfRouteConfig
		want string
	}{
		{SelfRouteConfig{Host: "elevator.albruiz.dev"}, "https://elevator.albruiz.dev/"},
		{SelfRouteConfig{Host: "elevator.albruiz.dev", PathPrefix: "/space-elevator/"}, "https://elevator.albruiz.dev/space-elevator/"},
		{SelfRouteConfig{Host: "elevator.albruiz.dev", PathPrefix: "space-elevator"}, "https://elevator.albruiz.dev/space-elevator/"},
		{SelfRouteConfig{}, ""},
	}
	for _, tc := range cases {
		got := tc.c.SelfURL()
		if got != tc.want {
			t.Errorf("SelfURL(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

