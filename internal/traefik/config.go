package traefik

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
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

var routeDomainPattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`)

// ValidDomain reports whether a custom domain is safe to embed in a
// Traefik Host rule.
func ValidDomain(domain string) bool {
	return len(domain) <= 253 && routeDomainPattern.MatchString(domain)
}

// Render produces the YAML body for a per-app dynamic file. For each backend
// it emits:
//
//   - A Host-only router pair (HTTP redirect → HTTPS secure) for every
//     custom domain attached to the service. If no domains are attached,
//     these are skipped.
//   - A Host+PathPrefix pair under PublicHost/AppPathPrefix/<app>/
//     (or /<app>-<service>/ for additional services) with a stripPrefix
//     middleware so the app is reachable under the dashboard host without a
//     dedicated DNS record.
//
// The HTTP routers redirect to HTTPS via a per-app <svc>-https middleware,
// matching the format used by the host's existing rootful routes.
func Render(cfg AppRouteConfig) ([]byte, error) {
	if err := validateRouteInputs(cfg); err != nil {
		return nil, err
	}
	dc := DynamicConfig{}
	dc.HTTP.Routers = map[string]Router{}
	dc.HTTP.Middlewares = map[string]Middleware{}
	dc.HTTP.Services = map[string]Service{}

	hasSubdomain := false
	hasPath := false
	prefix := normalizePrefix(cfg.AppPathPrefix)
	pathKeys := pathKeysFor(cfg.Routes)
	seenServices := map[string]bool{}
	seenPaths := map[string]bool{}

	for _, r := range cfg.Routes {
		if r.IP == "" || r.Port == 0 {
			continue
		}
		svcKey := serviceKey(r.AppName, r.Name)
		if seenServices[svcKey] {
			return nil, fmt.Errorf("route service key collision for %q", svcKey)
		}
		seenServices[svcKey] = true
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
				// net.JoinHostPort brackets IPv6 literals; plain
				// fmt.Sprintf("%s:%d") yields "http://fd00::2:8080",
				// which is not a parseable URL and makes Traefik drop the
				// whole file.
				Servers: []Server{{URL: "http://" + net.JoinHostPort(r.IP, strconv.Itoa(r.Port))}},
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
			pathKey := pathKeys[routeID(r)]
			if seenPaths[pathKey] {
				return nil, fmt.Errorf("route path key collision for %q", pathKey)
			}
			seenPaths[pathKey] = true
			appPrefix := prefix + pathKey + "/"
			rule := fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", cfg.PublicHost, appPrefix)
			dc.HTTP.Middlewares[stripMw] = Middleware{
				StripPrefix: &StripPrefix{Prefixes: []string{prefix + pathKey}},
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

func routeID(route ServiceRoute) string {
	return route.AppName + "\x00" + route.Name
}

// pathKeysFor gives a service named "web" the user-facing app path, while
// keeping other compose services addressable by app-service. This keeps the
// common static/web URL /app/my-app/ without changing service-specific paths
// for stacks that do not expose a web service.
func pathKeysFor(routes []ServiceRoute) map[string]string {
	byApp := make(map[string][]ServiceRoute)
	for _, route := range routes {
		byApp[route.AppName] = append(byApp[route.AppName], route)
	}

	keys := make(map[string]string, len(routes))
	for app, appRoutes := range byApp {
		sort.Slice(appRoutes, func(i, j int) bool { return appRoutes[i].Name < appRoutes[j].Name })
		primary := ""
		for _, route := range appRoutes {
			if route.Name == "web" {
				primary = route.Name
				break
			}
		}
		for _, route := range appRoutes {
			key := serviceKey(app, route.Name)
			if primary != "" && route.Name == primary {
				key = sanitize(app)
			}
			keys[routeID(route)] = key
		}
	}
	return keys
}

func serviceKey(app, svc string) string {
	return sanitize(app) + "-" + sanitize(svc)
}

func validateRouteInputs(cfg AppRouteConfig) error {
	if strings.ContainsAny(cfg.PublicHost, "`\r\n") || strings.ContainsAny(cfg.CertResolver, "`\r\n") {
		return fmt.Errorf("public host contains invalid characters")
	}
	if strings.ContainsAny(cfg.AppPathPrefix, "`\r\n") || strings.Contains(cfg.AppPathPrefix, "..") {
		return fmt.Errorf("app path prefix contains invalid characters")
	}
	for _, route := range cfg.Routes {
		if route.IP != "" && net.ParseIP(route.IP) == nil {
			return fmt.Errorf("invalid backend IP %q", route.IP)
		}
		if route.Port < 0 || route.Port > 65535 {
			return fmt.Errorf("invalid backend port %d", route.Port)
		}
		for _, domain := range route.Domain {
			if !ValidDomain(domain) {
				return fmt.Errorf("invalid route domain %q", domain)
			}
		}
	}
	return nil
}

func hostRule(domains []string) string {
	ordered := append([]string(nil), domains...)
	sort.Strings(ordered)
	parts := make([]string, len(ordered))
	for i, d := range ordered {
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
