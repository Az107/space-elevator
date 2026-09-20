package config

import (
	"os"
	"path/filepath"
	"regexp"
)

// Legacy holds values recovered from an existing pre-config-file install so
// `setup --from-legacy` / `config init --from-legacy` can preserve them.
type Legacy struct {
	BindAddr        string
	TraefikDir      string
	PublicHost      string
	PublicPath      string
	CertResolver    string
	RootlessGateway string
	Found           bool
}

var (
	legacyHost    = regexp.MustCompile("Host\\(`([^`]+)`\\)")
	legacyPath    = regexp.MustCompile("PathPrefix\\(`([^`]+)`\\)")
	legacyCert    = regexp.MustCompile(`certResolver:\s*(\S+)`)
	legacyGateway = regexp.MustCompile(`https?://(\d+\.\d+\.\d+\.\d+):\d+`)
)

// candidates lists the Traefik dynamic directories probed during legacy
// detection, in priority order.
func legacyTraefikDirs(home string) []string {
	return []string{
		filepath.Join(home, "Infra", "traefik", "rootful-dynamic"),
		filepath.Join(home, ".config", "traefik", "dynamic"),
		filepath.Join(home, "traefik", "dynamic"),
	}
}

// DetectLegacy looks for an existing Traefik dynamic directory and the routes
// space-elevator previously wrote, recovering the public host, cert resolver
// and rootless gateway when present.
func DetectLegacy() Legacy {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	var l Legacy
	for _, dir := range legacyTraefikDirs(home) {
		if _, err := os.Stat(dir); err == nil {
			l.TraefikDir = dir
			l.Found = true
			l.BindAddr = "0.0.0.0:8080"
			break
		}
	}
	if l.TraefikDir == "" {
		return l
	}
	if b, err := os.ReadFile(filepath.Join(l.TraefikDir, SelfRouteFile)); err == nil {
		if m := legacyHost.FindSubmatch(b); m != nil {
			l.PublicHost = string(m[1])
		}
		if m := legacyPath.FindSubmatch(b); m != nil {
			l.PublicPath = string(m[1])
		}
		if m := legacyCert.FindSubmatch(b); m != nil {
			l.CertResolver = string(m[1])
		}
	}
	entries, _ := os.ReadDir(l.TraefikDir)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(l.TraefikDir, e.Name()))
		if err != nil {
			continue
		}
		if m := legacyGateway.FindSubmatch(b); m != nil {
			l.RootlessGateway = string(m[1])
			break
		}
	}
	return l
}

// SelfRouteFile is the filename the dashboard self-route is written under.
// Duplicated from internal/traefik to avoid an import cycle.
const SelfRouteFile = "space-elevator.yml"

// ApplyLegacyResult overlays recovered values on top of a config that already
// has its own defaults, leaving untouched fields as-is.
func ApplyLegacy(c *Config, l Legacy) {
	if l.TraefikDir != "" {
		c.TraefikDir = l.TraefikDir
	}
	if l.PublicHost != "" {
		c.PublicHost = l.PublicHost
	}
	if l.PublicPath != "" {
		c.PublicPath = l.PublicPath
	}
	if l.CertResolver != "" {
		c.CertResolver = l.CertResolver
	}
	if l.RootlessGateway != "" {
		c.RootlessGateway = l.RootlessGateway
	}
	if l.BindAddr != "" {
		c.BindAddr = l.BindAddr
	}
}
