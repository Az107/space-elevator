package traefik

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SelfRouteConfig describes how the dashboard is exposed through Traefik.
//
// Host is the public hostname where the dashboard is reachable (e.g.
// "elevator.albruiz.dev"). PathPrefix, when non-empty, narrows the route to a
// sub-path on that host (e.g. "/space-elevator/") so the dashboard can share
// the host with other services. BackendURL is where Traefik forwards
// dashboard traffic (e.g. "http://host.containers.internal:8080").
// CertResolver is the Let's Encrypt resolver name declared by the host's
// Traefik static config.
//
// At least one of Host or PathPrefix must be set; otherwise RenderSelf
// refuses to produce a configuration that would catch every request.
type SelfRouteConfig struct {
	Host         string
	PathPrefix   string
	BackendURL   string
	CertResolver string
}

// SelfURL returns the public URL where the dashboard can be reached, using
// the Host + optional PathPrefix. Useful for the "open in browser" hint and
// the settings page.
func (c SelfRouteConfig) SelfURL() string {
	host := c.Host
	if host == "" {
		return ""
	}
	p := strings.TrimRight(c.PathPrefix, "/")
	if p == "" {
		return "https://" + host + "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "https://" + host + p + "/"
}

// RenderSelf produces a Traefik dynamic YAML file exposing the dashboard.
// It mirrors the format of the host's existing rootful routes: a `web` router
// that redirects to HTTPS via the per-route middleware, and a `websecure`
// router that terminates TLS with the given certResolver.
//
// Rules emitted:
//   - Host only: Host(`host`)
//   - Path only: PathPrefix(`/prefix/`)
//   - Both: Host(`host`) && PathPrefix(`/prefix/`)
//
// An empty result (no rule produced) signals the caller to remove the file.
func RenderSelf(c SelfRouteConfig) ([]byte, error) {
	if c.BackendURL == "" {
		return nil, fmt.Errorf("self-route: backend URL is required")
	}
	if c.Host == "" && c.PathPrefix == "" {
		return nil, fmt.Errorf("self-route: at least one of Host or PathPrefix is required")
	}
	if c.CertResolver == "" {
		return nil, fmt.Errorf("self-route: cert resolver is required")
	}

	prefix := normalizePrefix(c.PathPrefix)
	var rule string
	switch {
	case c.Host != "" && prefix != "":
		rule = fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", c.Host, prefix)
	case c.Host != "":
		rule = fmt.Sprintf("Host(`%s`)", c.Host)
	default:
		rule = fmt.Sprintf("PathPrefix(`%s`)", prefix)
	}

	mwKey := SelfRouteFileName
	mwKey = strings.TrimSuffix(mwKey, ".yml")
	httpsMw := mwKey + "-https"

	dc := DynamicConfig{}
	dc.HTTP.Routers = map[string]Router{
		mwKey + "-secure": {
			Rule:        rule,
			EntryPoints: []string{"websecure"},
			Service:     mwKey,
			TLS:         &TLSConfig{CertResolver: c.CertResolver},
		},
		mwKey + "-web": {
			Rule:        rule,
			EntryPoints: []string{"web"},
			Service:     mwKey,
			Middlewares: []string{httpsMw},
		},
	}
	dc.HTTP.Middlewares = map[string]Middleware{
		httpsMw: {
			RedirectScheme: &RedirectScheme{Scheme: "https", Permanent: true},
		},
	}
	dc.HTTP.Services = map[string]Service{
		mwKey: {
			LoadBalancer: LoadBalancer{
				Servers: []Server{{URL: c.BackendURL}},
			},
		},
	}

	out, err := yaml.Marshal(dc)
	if err != nil {
		return nil, fmt.Errorf("marshal self-route: %w", err)
	}
	return out, nil
}
