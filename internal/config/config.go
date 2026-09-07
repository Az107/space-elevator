package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	SocketPath       string
	DataDir          string
	StateDir         string
	TraefikDir       string
	PublicHost       string
	PublicPath       string
	DashboardURL     string
	CertResolver     string
	QuadletDir       string
	AppsRoot         string
	DefaultNetwork   string
	AppPathPrefix    string
	// RootlessGateway is the IP that the host exposes the rootless podman
	// network on. From the rootful Traefik container's perspective,
	// reaching "<RootlessGateway>:<host-published-port>" hits the
	// host-published port (which forwards into the rootless container).
	// 10.89.0.1 is the default for rootless Podman using the default
	// slirp4netns bridge.
	RootlessGateway string
	// Container defaults applied at deploy time. "0" means "no limit"
	// — the kernel applies only the cgroup default.
	DefaultMemoryBytes int64
	DefaultPidsLimit    int64
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	return &Config{
		SocketPath:         defaultSocket(),
		DataDir:            filepath.Join(home, ".local", "share", "space-elevator"),
		StateDir:           filepath.Join(home, ".local", "state", "space-elevator"),
		TraefikDir:         envOr("SPACE_ELEVATOR_TRAEFIK_DIR", filepath.Join(home, "Infra", "traefik", "rootful-dynamic")),
		PublicHost:         envOr("SPACE_ELEVATOR_PUBLIC_HOST", "elevator.albruiz.dev"),
		PublicPath:         envOr("SPACE_ELEVATOR_PUBLIC_PATH", ""),
		DashboardURL:       envOr("SPACE_ELEVATOR_DASHBOARD_URL", "http://host.containers.internal:8080"),
		CertResolver:       envOr("SPACE_ELEVATOR_CERT_RESOLVER", "letsencrypt"),
		QuadletDir:         filepath.Join(home, ".config", "containers", "systemd"),
		AppsRoot:           envOr("SPACE_ELEVATOR_APPS_ROOT", filepath.Join(home, "apps")),
		DefaultNetwork:     "space-elevator",
		AppPathPrefix:      envOr("SPACE_ELEVATOR_APP_PATH_PREFIX", "/app/"),
		RootlessGateway:    envOr("SPACE_ELEVATOR_ROOTLESS_GATEWAY", "10.89.0.1"),
		DefaultMemoryBytes: parseSizeBytes(envOr("SPACE_ELEVATOR_MEMORY_LIMIT", "512M")),
		DefaultPidsLimit:   parseInt64(envOr("SPACE_ELEVATOR_PIDS_LIMIT", "256")),
	}
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
		filepath.Join("/run/user", fmt.Sprintf("%d", uid), "podman", "podman.sock"),
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
	dirs := []string{c.DataDir, c.StateDir, c.AppsRoot}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// Traefik dir needs sudo in normal setups; attempt and continue on failure.
	_ = os.MkdirAll(c.TraefikDir, 0o755)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
