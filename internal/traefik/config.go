package traefik

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServiceRoute is one (app, compose-service) backend that should be exposed
// through Traefik. A given route is exposed under both any attached custom
// domains (subdomain-style) and a path-prefix under the public host.
type ServiceRoute struct {
	AppName string
	Name    string // compose service name
	Domain  []string
	IP      string // container IP on app network
	Port    int    // container port
}

type DynamicConfig struct {
	HTTP struct {
		Routers     map[string]Router     `yaml:"routers,omitempty"`
		Middlewares map[string]Middleware `yaml:"middlewares,omitempty"`
		Services    map[string]Service    `yaml:"services,omitempty"`
	} `yaml:"http"`
}

type Router struct {
	Rule        string     `yaml:"rule"`
	EntryPoints []string   `yaml:"entryPoints"`
	Service     string     `yaml:"service"`
	Middlewares []string   `yaml:"middlewares,omitempty"`
	TLS         *TLSConfig `yaml:"tls,omitempty"`
	Priority    int        `yaml:"priority,omitempty"`
}

type Middleware struct {
	RedirectScheme *RedirectScheme `yaml:"redirectScheme,omitempty"`
	StripPrefix    *StripPrefix    `yaml:"stripPrefix,omitempty"`
}

type RedirectScheme struct {
	Scheme    string `yaml:"scheme"`
	Permanent bool   `yaml:"permanent"`
}

type StripPrefix struct {
	Prefixes []string `yaml:"prefixes"`
}

type Service struct {
	LoadBalancer LoadBalancer `yaml:"loadBalancer"`
}

type LoadBalancer struct {
	Servers []Server `yaml:"servers"`
}

type Server struct {
	URL string `yaml:"url"`
}

type TLSConfig struct {
	CertResolver string `yaml:"certResolver"`
}

// AppRouteConfig bundles the inputs needed to render a per-app dynamic file:
// the resolved backend routes, the public host under which path-prefix routes
// are emitted (e.g. "elevator.albruiz.dev"), and the path-prefix itself
// (e.g. "/app/"). An empty PublicHost or AppPathPrefix disables path-prefix
// routing for the file.
type AppRouteConfig struct {
	Routes        []ServiceRoute
	PublicHost    string
	AppPathPrefix string
	CertResolver  string
}

// Render produces the YAML body for a per-app dynamic file. For each backend
// it emits:
//
//   - A Host-only router pair (HTTP redirect → HTTPS secure) for every
//     custom domain attached to the service. If no domains are attached,
//     these are skipped.
//   - A Host+PathPrefix pair under PublicHost/AppPathPrefix/<app>-<service>/
//     (with a stripPrefix middleware) so the app is reachable under the
//     dashboard subdomain without a dedicated DNS record.
//
// The HTTP routers redirect to HTTPS via a per-app <svc>-https middleware,
// matching the format used by the host's existing rootful routes.
func Render(cfg AppRouteConfig) ([]byte, error) {
	dc := DynamicConfig{}
	dc.HTTP.Routers = map[string]Router{}
	dc.HTTP.Middlewares = map[string]Middleware{}
	dc.HTTP.Services = map[string]Service{}

	hasSubdomain := false
	hasPath := false
	prefix := normalizePrefix(cfg.AppPathPrefix)

	for _, r := range cfg.Routes {
		if r.IP == "" || r.Port == 0 {
			continue
		}
		svcKey := serviceKey(r.AppName, r.Name)
		secure := cfg.CertResolver != ""
		httpsMw := svcKey + "-https"
		stripMw := svcKey + "-strip"

		if secure {
			dc.HTTP.Middlewares[httpsMw] = Middleware{
				RedirectScheme: &RedirectScheme{Scheme: "https", Permanent: true},
			}
		}
		dc.HTTP.Services[svcKey] = Service{
			LoadBalancer: LoadBalancer{
				Servers: []Server{{URL: fmt.Sprintf("http://%s:%d", r.IP, r.Port)}},
			},
		}

		if len(r.Domain) > 0 {
			hasSubdomain = true
			rule := hostRule(r.Domain)
			if secure {
				dc.HTTP.Routers[svcKey+"-secure"] = Router{
					Rule:        rule,
					EntryPoints: []string{"websecure"},
					Service:     svcKey,
					TLS:         &TLSConfig{CertResolver: cfg.CertResolver},
				}
				dc.HTTP.Routers[svcKey+"-web"] = Router{
					Rule:        rule,
					EntryPoints: []string{"web"},
					Service:     svcKey,
					Middlewares: []string{httpsMw},
				}
			} else {
				dc.HTTP.Routers[svcKey+"-web"] = Router{
					Rule:        rule,
					EntryPoints: []string{"web"},
					Service:     svcKey,
				}
			}
		}

		if cfg.PublicHost != "" && prefix != "" {
			hasPath = true
			appPrefix := prefix + svcKey + "/"
			rule := fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", cfg.PublicHost, appPrefix)
			dc.HTTP.Middlewares[stripMw] = Middleware{
				StripPrefix: &StripPrefix{Prefixes: []string{prefix + svcKey}},
			}
			if secure {
				dc.HTTP.Routers[svcKey+"-path-secure"] = Router{
					Rule:        rule,
					EntryPoints: []string{"websecure"},
					Service:     svcKey,
					Middlewares: []string{stripMw},
					TLS:         &TLSConfig{CertResolver: cfg.CertResolver},
				}
				dc.HTTP.Routers[svcKey+"-path-web"] = Router{
					Rule:        rule,
					EntryPoints: []string{"web"},
					Service:     svcKey,
					Middlewares: []string{stripMw, httpsMw},
				}
			} else {
				dc.HTTP.Routers[svcKey+"-path-web"] = Router{
					Rule:        rule,
					EntryPoints: []string{"web"},
					Service:     svcKey,
					Middlewares: []string{stripMw},
				}
			}
		}
	}

	if !hasSubdomain && !hasPath {
		return []byte{}, nil
	}

	out, err := yaml.Marshal(dc)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return out, nil
}

func serviceKey(app, svc string) string {
	return sanitize(app) + "-" + sanitize(svc)
}

func hostRule(domains []string) string {
	sort.Strings(domains)
	parts := make([]string, len(domains))
	for i, d := range domains {
		parts[i] = "Host(`" + d + "`)"
	}
	return strings.Join(parts, " || ")
}

// normalizePrefix turns "" or "/" into "", "/x" into "/x/", "/x/" into "/x/".
func normalizePrefix(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p = p + "/"
	}
	return p
}

func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return strings.ToLower(b.String())
}
