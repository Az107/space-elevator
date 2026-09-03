package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	SocketPath     string
	DataDir        string
	StateDir       string
	TraefikDir     string
	QuadletDir     string
	AppsRoot       string
	DefaultNetwork string
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	return &Config{
		SocketPath:     defaultSocket(),
		DataDir:        filepath.Join(home, ".local", "share", "space-elevator"),
		StateDir:       filepath.Join(home, ".local", "state", "space-elevator"),
		TraefikDir:     "/etc/space-elevator/traefik/dynamic",
		QuadletDir:     filepath.Join(home, ".config", "containers", "systemd"),
		AppsRoot:       filepath.Join(home, "apps"),
		DefaultNetwork: "space-elevator",
	}
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
	dirs := []string{c.DataDir, c.StateDir, c.TraefikDir, c.QuadletDir, c.AppsRoot}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}