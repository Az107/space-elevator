package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the fully-resolved runtime configuration. Every field can be set
// (in increasing priority) by the YAML config file, a SPACE_ELEVATOR_* env
// var, or a CLI flag. Empty PublicHost, TraefikDir and CertResolver are
// meaningful: they disable that integration rather than falling back to a
// site-specific default.
type Config struct {
	// BindAddr is where `serve` listens (host:port).
	BindAddr string
	// SocketPath is the Podman/Docker API socket.
	SocketPath string
	// DataDir holds long-lived data (uploads, checkouts).
	DataDir string
	// StateDir holds the SQLite DB and CSRF key (owner-only).
	StateDir string
	// TraefikDir is the directory watched by a Traefik file provider. Empty
	// disables Traefik route generation.
	TraefikDir string
	// PublicHost is the hostname the dashboard and path-prefix app routes are
	// served under. Empty disables path-prefix routing and the self-route.
	PublicHost string
	// PublicPath optionally narrows the dashboard route to a sub-path.
	PublicPath string
	// DashboardURL is the backend URL Traefik forwards dashboard traffic to.
	// Derived from BindAddr when empty.
	DashboardURL string
	// CertResolver is the Traefik TLS certificate resolver name (e.g.
	// "letsencrypt"). Empty disables TLS settings in generated routes.
	CertResolver string
	// QuadletDir is where systemd quadlet files live (reserved).
	QuadletDir string
	// AppsRoot is where app drops and checkouts live.
	AppsRoot string
	// DefaultNetwork is the Podman network apps are attached to.
	DefaultNetwork string
	// AppPathPrefix is the path prefix under PublicHost (default "/app/").
	AppPathPrefix string
	// RootlessGateway is the IP the host exposes the rootless Podman network
	// on, reachable from a rootful Traefik container. Empty falls back to the
	// container IP.
	RootlessGateway string
	// DefaultMemoryBytes / DefaultPidsLimit are per-container defaults.
	// "0" means no limit.
	DefaultMemoryBytes int64
	DefaultPidsLimit   int64
	// InsecureCookies disables the Secure flag on the session cookie. Only for
	// plain-HTTP local testing.
	InsecureCookies bool
}

// BuiltinDefaults returns the neutral, host-agnostic defaults. It does not
// read the config file or the environment. Use Load (or Default) for the
// effective configuration.
func BuiltinDefaults() *Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	return &Config{
		BindAddr:           "127.0.0.1:8080",
		SocketPath:         defaultSocket(),
		DataDir:            filepath.Join(home, ".local", "share", "space-elevator"),
		StateDir:           filepath.Join(home, ".local", "state", "space-elevator"),
		TraefikDir:         "",
		PublicHost:         "",
		PublicPath:         "",
		DashboardURL:       "",
		CertResolver:       "",
		QuadletDir:         filepath.Join(home, ".config", "containers", "systemd"),
		AppsRoot:           filepath.Join(home, "apps"),
		DefaultNetwork:     "space-elevator",
		AppPathPrefix:      "/app/",
		RootlessGateway:    "",
		DefaultMemoryBytes: 512 << 20,
		DefaultPidsLimit:   256,
		InsecureCookies:    false,
	}
}

// Default returns the effective configuration: built-in defaults overlaid by
// the YAML config file (see ConfigPath) and SPACE_ELEVATOR_* env vars. CLI
// flags are applied by the caller. It exits with a clear message if the config
// file exists but cannot be read or parsed.
//
// This is the convenience entry point the CLI commands use; new code that can
// handle errors should call Load directly.
func Default() *Config {
	cfg, err := Load("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "space-elevator: "+err.Error())
		os.Exit(1)
	}
	return cfg
}

// ConfigPath returns the config file location. SPACE_ELEVATOR_CONFIG wins,
// then $XDG_CONFIG_HOME/space-elevator/config.yaml, then ~/.config/...
func ConfigPath() string {
	if v := os.Getenv("SPACE_ELEVATOR_CONFIG"); v != "" {
		return v
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/root"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "space-elevator", "config.yaml")
}

// Load resolves the configuration with priority flags > env > file > defaults.
// An explicit, non-empty path that does not exist is an error; the default
// path may be absent.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if path == "" {
		path = ConfigPath()
	}
	cfg := BuiltinDefaults()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := applyYAML(cfg, data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		if explicit {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	applyEnv(cfg)
	cfg.finalize()
	return cfg, nil
}

// MustLoad panics on error; convenient for tests and internal calls where the
// config has already been validated.
func MustLoad(path string) *Config {
	cfg, err := Load(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// finalize fills in derived values and normalizes prefixes.
func (c *Config) finalize() {
	if c.DashboardURL == "" {
		_, port, err := net.SplitHostPort(c.BindAddr)
		if err != nil || port == "" {
			port = "8080"
		}
		if c.TraefikDir != "" {
			// A rootful Traefik container reaches the host via this name.
			c.DashboardURL = "http://host.containers.internal:" + port
		} else {
			c.DashboardURL = "http://127.0.0.1:" + port
		}
	}
	if c.AppPathPrefix != "" {
		if !strings.HasPrefix(c.AppPathPrefix, "/") {
			c.AppPathPrefix = "/" + c.AppPathPrefix
		}
		if !strings.HasSuffix(c.AppPathPrefix, "/") {
			c.AppPathPrefix += "/"
		}
	}
}

// Issue is a validation finding. Level is "error" or "warning".
type Issue struct {
	Level   string
	Message string
}

// Validate reports configuration problems. Errors block startup; warnings are
// advisory and are surfaced by `doctor` and `serve`.
func (c *Config) Validate() []Issue {
	var out []Issue
	if c.BindAddr == "" {
		out = append(out, Issue{"error", "bind_addr is empty"})
	} else if _, _, err := net.SplitHostPort(c.BindAddr); err != nil {
		out = append(out, Issue{"error", fmt.Sprintf("bind_addr %q is not host:port", c.BindAddr)})
	}
	if c.DefaultMemoryBytes < 0 {
		out = append(out, Issue{"error", "memory limit cannot be negative"})
	}
	if c.DefaultPidsLimit < 0 {
		out = append(out, Issue{"error", "pids limit cannot be negative"})
	}
	if c.PublicHost != "" && c.TraefikDir == "" {
		out = append(out, Issue{"warning", "public_host is set but traefik_dir is empty: apps are only reachable via published host ports"})
	}
	if c.PublicHost != "" && c.CertResolver == "" {
		out = append(out, Issue{"warning", "public_host is set but cert_resolver is empty: routes will not request a certificate"})
	}
	if c.TraefikDir != "" && c.CertResolver == "" {
		out = append(out, Issue{"warning", "traefik_dir is set but cert_resolver is empty: Traefik routes will be emitted without TLS"})
	}
	if c.PublicHost == "" && c.TraefikDir != "" {
		out = append(out, Issue{"warning", "traefik_dir is set but public_host is empty: no dashboard self-route will be written"})
	}
	if c.PublicHost == "" && c.PublicPath != "" {
		out = append(out, Issue{"warning", "public_path has no effect without public_host"})
	}
	return out
}

// Errors returns only the error-level issues.
func (c *Config) Errors() []string {
	var out []string
	for _, i := range c.Validate() {
		if i.Level == "error" {
			out = append(out, i.Message)
		}
	}
	return out
}

// parseSizeBytes accepts a human-friendly size like "512M", "1G", "0" and
// returns bytes. "0" or "" parses to 0 (no limit).
func parseSizeBytes(s string) int64 {
	if s == "" || s == "0" {
		return 0
	}
	n := len(s)
	if n < 2 {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
	last := s[n-1]
	mult := int64(1)
	switch last {
	case 'K', 'k':
		mult = 1024
		s = s[:n-1]
	case 'M', 'm':
		mult = 1024 * 1024
		s = s[:n-1]
	case 'G', 'g':
		mult = 1024 * 1024 * 1024
		s = s[:n-1]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * mult
}

// FormatSizeBytes renders a byte count back to the compact "512M" form.
func FormatSizeBytes(v int64) string {
	switch {
	case v == 0:
		return "0"
	case v%(1024*1024*1024) == 0:
		return strconv.FormatInt(v/(1024*1024*1024), 10) + "G"
	case v%(1024*1024) == 0:
		return strconv.FormatInt(v/(1024*1024), 10) + "M"
	case v%1024 == 0:
		return strconv.FormatInt(v/1024, 10) + "K"
	default:
		return strconv.FormatInt(v, 10)
	}
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func defaultSocket() string {
	if v := os.Getenv("PODMAN_SOCKET"); v != "" {
		return v
	}
	if uid := os.Getuid(); uid == 0 {
		return "/run/podman/podman.sock"
	}
	uid := os.Getuid()
	candidates := []string{
		filepath.Join("/run/user", strconv.Itoa(uid), "podman", "podman.sock"),
		filepath.Join(os.TempDir(), "podman", "podman.sock"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

func (c *Config) EnsureDirs() error {
	// StateDir holds the SQLite DB (password hashes, git tokens) and the
	// CSRF key — keep it owner-only.
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{c.DataDir, 0o755},
		{c.StateDir, 0o700},
		{c.AppsRoot, 0o755},
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return err
		}
	}
	// Traefik dir needs sudo in normal setups; attempt and continue on failure.
	if c.TraefikDir != "" {
		_ = os.MkdirAll(c.TraefikDir, 0o755)
	}
	return nil
}

func envStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func applyEnv(c *Config) {
	envStr(&c.BindAddr, "SPACE_ELEVATOR_BIND_ADDR")
	envStr(&c.SocketPath, "PODMAN_SOCKET")
	envStr(&c.DataDir, "SPACE_ELEVATOR_DATA_DIR")
	envStr(&c.StateDir, "SPACE_ELEVATOR_STATE_DIR")
	envStr(&c.TraefikDir, "SPACE_ELEVATOR_TRAEFIK_DIR")
	envStr(&c.PublicHost, "SPACE_ELEVATOR_PUBLIC_HOST")
	envStr(&c.PublicPath, "SPACE_ELEVATOR_PUBLIC_PATH")
	envStr(&c.DashboardURL, "SPACE_ELEVATOR_DASHBOARD_URL")
	envStr(&c.CertResolver, "SPACE_ELEVATOR_CERT_RESOLVER")
	envStr(&c.QuadletDir, "SPACE_ELEVATOR_QUADLET_DIR")
	envStr(&c.AppsRoot, "SPACE_ELEVATOR_APPS_ROOT")
	envStr(&c.DefaultNetwork, "SPACE_ELEVATOR_DEFAULT_NETWORK")
	envStr(&c.AppPathPrefix, "SPACE_ELEVATOR_APP_PATH_PREFIX")
	envStr(&c.RootlessGateway, "SPACE_ELEVATOR_ROOTLESS_GATEWAY")
	if v, ok := os.LookupEnv("SPACE_ELEVATOR_MEMORY_LIMIT"); ok {
		c.DefaultMemoryBytes = parseSizeBytes(v)
	}
	if v, ok := os.LookupEnv("SPACE_ELEVATOR_PIDS_LIMIT"); ok {
		c.DefaultPidsLimit = parseInt64(v)
	}
	if v, ok := os.LookupEnv("SPACE_ELEVATOR_INSECURE_COOKIES"); ok {
		c.InsecureCookies = v != ""
	}
}

// EnvKeys lists every environment variable Load honors, for `config show`.
var EnvKeys = []string{
	"SPACE_ELEVATOR_CONFIG",
	"SPACE_ELEVATOR_BIND_ADDR",
	"PODMAN_SOCKET",
	"SPACE_ELEVATOR_DATA_DIR",
	"SPACE_ELEVATOR_STATE_DIR",
	"SPACE_ELEVATOR_TRAEFIK_DIR",
	"SPACE_ELEVATOR_PUBLIC_HOST",
	"SPACE_ELEVATOR_PUBLIC_PATH",
	"SPACE_ELEVATOR_DASHBOARD_URL",
	"SPACE_ELEVATOR_CERT_RESOLVER",
	"SPACE_ELEVATOR_QUADLET_DIR",
	"SPACE_ELEVATOR_APPS_ROOT",
	"SPACE_ELEVATOR_DEFAULT_NETWORK",
	"SPACE_ELEVATOR_APP_PATH_PREFIX",
	"SPACE_ELEVATOR_ROOTLESS_GATEWAY",
	"SPACE_ELEVATOR_MEMORY_LIMIT",
	"SPACE_ELEVATOR_PIDS_LIMIT",
	"SPACE_ELEVATOR_INSECURE_COOKIES",
}
