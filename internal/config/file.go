package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"gopkg.in/yaml.v3"
)

// fileConfig mirrors Config for YAML, using pointers so an explicitly empty
// value (e.g. `public_host: ""`) is distinguishable from an absent key.
type fileConfig struct {
	BindAddr                 *string `yaml:"bind_addr"`
	SocketPath               *string `yaml:"socket_path"`
	DataDir                  *string `yaml:"data_dir"`
	BackupDir                *string `yaml:"backup_dir"`
	StateDir                 *string `yaml:"state_dir"`
	TraefikDir               *string `yaml:"traefik_dir"`
	PublicHost               *string `yaml:"public_host"`
	PublicPath               *string `yaml:"public_path"`
	DashboardURL             *string `yaml:"dashboard_url"`
	CertResolver             *string `yaml:"cert_resolver"`
	QuadletDir               *string `yaml:"quadlet_dir"`
	AppsRoot                 *string `yaml:"apps_root"`
	DefaultNetwork           *string `yaml:"default_network"`
	AppPathPrefix            *string `yaml:"app_path_prefix"`
	RootlessGateway          *string `yaml:"rootless_gateway"`
	MemoryLimit              *string `yaml:"memory_limit"`
	PidsLimit                *int64  `yaml:"pids_limit"`
	InsecureCookies          *bool   `yaml:"insecure_cookies"`
	UpdateRepo               *string `yaml:"update_repo"`
	TokenManagerURL          *string `yaml:"token_manager_url"`
	TokenManagerClientID     *string `yaml:"token_manager_client_id"`
	TokenManagerClientSecret *string `yaml:"token_manager_client_secret"`
}

func applyYAML(c *Config, data []byte) error {
	var f fileConfig
	if err := yaml.Unmarshal(data, &f); err != nil {
		return err
	}
	if f.BindAddr != nil {
		c.BindAddr = *f.BindAddr
	}
	if f.SocketPath != nil {
		c.SocketPath = *f.SocketPath
	}
	if f.DataDir != nil {
		c.DataDir = *f.DataDir
	}
	if f.BackupDir != nil {
		c.BackupDir = *f.BackupDir
	}
	if f.StateDir != nil {
		c.StateDir = *f.StateDir
	}
	if f.TraefikDir != nil {
		c.TraefikDir = *f.TraefikDir
	}
	if f.PublicHost != nil {
		c.PublicHost = *f.PublicHost
	}
	if f.PublicPath != nil {
		c.PublicPath = *f.PublicPath
	}
	if f.DashboardURL != nil {
		c.DashboardURL = *f.DashboardURL
	}
	if f.CertResolver != nil {
		c.CertResolver = *f.CertResolver
	}
	if f.QuadletDir != nil {
		c.QuadletDir = *f.QuadletDir
	}
	if f.AppsRoot != nil {
		c.AppsRoot = *f.AppsRoot
	}
	if f.DefaultNetwork != nil {
		c.DefaultNetwork = *f.DefaultNetwork
	}
	if f.AppPathPrefix != nil {
		c.AppPathPrefix = *f.AppPathPrefix
	}
	if f.RootlessGateway != nil {
		c.RootlessGateway = *f.RootlessGateway
	}
	if f.MemoryLimit != nil {
		size, err := parseSizeBytes(*f.MemoryLimit)
		if err != nil {
			return fmt.Errorf("memory_limit: %w", err)
		}
		c.DefaultMemoryBytes = size
	}
	if f.PidsLimit != nil {
		n, err := parseInt64(strconv.FormatInt(*f.PidsLimit, 10))
		if err != nil {
			return fmt.Errorf("pids_limit: %w", err)
		}
		c.DefaultPidsLimit = n
	}
	if f.InsecureCookies != nil {
		c.InsecureCookies = *f.InsecureCookies
	}
	if f.UpdateRepo != nil {
		c.UpdateRepo = *f.UpdateRepo
	}
	if f.TokenManagerURL != nil {
		c.TokenManagerURL = *f.TokenManagerURL
	}
	if f.TokenManagerClientID != nil {
		c.TokenManagerClientID = *f.TokenManagerClientID
	}
	if f.TokenManagerClientSecret != nil {
		c.TokenManagerClientSecret = *f.TokenManagerClientSecret
	}
	return nil
}

// Save writes a fully-commented config file reflecting c. The generated file
// is intended to be hand-edited later; every key is documented inline.
func (c *Config) Save(path string) error {
	if path == "" {
		path = ConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlinked config %q", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var b bytes.Buffer
	if err := c.SaveTo(&b); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".space-elevator-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(b.Bytes()); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// Save may be replacing a pre-existing config created with the old
	// world-readable mode. Ensure the file remains private after adding
	// Token-Manager credentials.
	return os.Chmod(path, 0o600)
}

// SaveTo renders the commented config to w without touching the filesystem.
func (c *Config) SaveTo(w io.Writer) error {
	c.finalize()
	return configTemplate.Execute(w, templateData(c))
}

type tmplData struct {
	BindAddr, SocketPath, DataDir, BackupDir, StateDir string
	TraefikDir, PublicHost, PublicPath                 string
	DashboardURL, CertResolver, QuadletDir             string
	AppsRoot, DefaultNetwork, AppPathPrefix            string
	RootlessGateway                                    string
	MemoryLimit                                        string
	PidsLimit                                          int64
	InsecureCookies                                    bool
	UpdateRepo                                         string
	TokenManagerURL                                    string
	TokenManagerClientID                               string
	TokenManagerClientSecret                           string
}

func templateData(c *Config) tmplData {
	return tmplData{
		BindAddr:                 c.BindAddr,
		SocketPath:               c.SocketPath,
		DataDir:                  c.DataDir,
		BackupDir:                c.BackupDir,
		StateDir:                 c.StateDir,
		TraefikDir:               c.TraefikDir,
		PublicHost:               c.PublicHost,
		PublicPath:               c.PublicPath,
		DashboardURL:             c.DashboardURL,
		CertResolver:             c.CertResolver,
		QuadletDir:               c.QuadletDir,
		AppsRoot:                 c.AppsRoot,
		DefaultNetwork:           c.DefaultNetwork,
		AppPathPrefix:            c.AppPathPrefix,
		RootlessGateway:          c.RootlessGateway,
		MemoryLimit:              FormatSizeBytes(c.DefaultMemoryBytes),
		PidsLimit:                c.DefaultPidsLimit,
		InsecureCookies:          c.InsecureCookies,
		UpdateRepo:               c.UpdateRepo,
		TokenManagerURL:          c.TokenManagerURL,
		TokenManagerClientID:     c.TokenManagerClientID,
		TokenManagerClientSecret: c.TokenManagerClientSecret,
	}
}

var configTemplate = template.Must(template.New("config").Funcs(template.FuncMap{
	"q": strconv.Quote,
}).Parse(`# space-elevator configuration
#
# Generated by ` + "`space-elevator setup`" + `. Every key can be overridden by
# the matching SPACE_ELEVATOR_* environment variable, and a CLI flag wins over
# both. Full reference: docs/configuration.md
#
# Priority:  CLI flag  >  environment variable  >  this file  >  built-in default
#
# The file lives at $XDG_CONFIG_HOME/space-elevator/config.yaml
# (default ~/.config/space-elevator/config.yaml).

# Address the dashboard listens on (host:port). Use 0.0.0.0:8080 when a
# rootful Traefik container has to reach the dashboard through the host.
# env: SPACE_ELEVATOR_BIND_ADDR
bind_addr: {{q .BindAddr}}

# Podman/Docker API socket. Leave empty to auto-detect
# ($XDG_RUNTIME_DIR/podman/podman.sock, falling back to /run/podman/podman.sock).
# env: PODMAN_SOCKET
socket_path: {{q .SocketPath}}

# Long-lived data (uploads, git checkouts).
# env: SPACE_ELEVATOR_DATA_DIR
data_dir: {{q .DataDir}}

# App update backups. Keep this on storage with enough room for database volumes.
# env: SPACE_ELEVATOR_BACKUP_DIR
backup_dir: {{q .BackupDir}}

# SQLite DB + CSRF key (created 0700).
# env: SPACE_ELEVATOR_STATE_DIR
state_dir: {{q .StateDir}}

# Directory watched by a Traefik file provider. Leave empty to disable Traefik
# integration; apps are then reachable only on their published host ports.
# env: SPACE_ELEVATOR_TRAEFIK_DIR
traefik_dir: {{q .TraefikDir}}

# Public hostname for the dashboard and /app/<name>/ routes. Leave empty for
# local-only access. A wildcard DNS record pointing here is required for app
# routes to resolve.
# env: SPACE_ELEVATOR_PUBLIC_HOST
public_host: {{q .PublicHost}}

# Optional sub-path for the dashboard route, e.g. /space-elevator/ when the
# dashboard shares a host with other services.
# env: SPACE_ELEVATOR_PUBLIC_PATH
public_path: {{q .PublicPath}}

# Backend URL Traefik forwards dashboard traffic to. Leave empty to derive it
# from bind_addr (host.containers.internal when Traefik is enabled).
# env: SPACE_ELEVATOR_DASHBOARD_URL
dashboard_url: {{q .DashboardURL}}

# Name of the Traefik TLS certificate resolver (e.g. "letsencrypt"). Leave
# empty to emit routes without TLS.
# env: SPACE_ELEVATOR_CERT_RESOLVER
cert_resolver: {{q .CertResolver}}

# Where systemd quadlet files live (reserved for later use).
# env: SPACE_ELEVATOR_QUADLET_DIR
quadlet_dir: {{q .QuadletDir}}

# Root directory for app drops and checkouts.
# env: SPACE_ELEVATOR_APPS_ROOT
apps_root: {{q .AppsRoot}}

# Podman network apps are attached to.
# env: SPACE_ELEVATOR_DEFAULT_NETWORK
default_network: {{q .DefaultNetwork}}

# Path prefix for app routes under public_host.
# env: SPACE_ELEVATOR_APP_PATH_PREFIX
app_path_prefix: {{q .AppPathPrefix}}

# IP a rootful Traefik container uses to reach host-published ports of rootless
# containers. Usually the rootless bridge gateway, e.g. 10.89.0.1. Leave empty
# to use the container IP directly (only works on a shared network).
# env: SPACE_ELEVATOR_ROOTLESS_GATEWAY
rootless_gateway: {{q .RootlessGateway}}

# Per-container memory limit (0 = unlimited).
# env: SPACE_ELEVATOR_MEMORY_LIMIT
memory_limit: {{q .MemoryLimit}}

# Per-container PID limit (0 = unlimited).
# env: SPACE_ELEVATOR_PIDS_LIMIT
pids_limit: {{.PidsLimit}}

# Disable the Secure flag on the session cookie. ONLY for plain-HTTP local
# testing; leave false in production.
# env: SPACE_ELEVATOR_INSECURE_COOKIES
insecure_cookies: {{.InsecureCookies}}

# GitHub repository that "space-elevator update" checks for new releases, e.g.
# https://github.com/owner/repo. Leave empty to disable the updater; a bad
# value is only a warning. Only GitHub releases are supported.
# env: SPACE_ELEVATOR_UPDATE_REPO
update_repo: {{q .UpdateRepo}}

# External Token-Manager base URL. Leave all three Token-Manager values empty
# to keep local dashboard access working while the REST API remains disabled.
# The service validates /api/v1 bearer tokens; space-elevator never stores
# their plaintext.
# env: SPACE_ELEVATOR_TOKEN_MANAGER_URL
token_manager_url: {{q .TokenManagerURL}}

# Client ID and secret for the app registered in Token-Manager. Create an
# app named space-elevator and copy its one-time credentials here. Keep this
# file private; the secret is redacted by config show.
# env: SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_ID
token_manager_client_id: {{q .TokenManagerClientID}}
# env: SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_SECRET
token_manager_client_secret: {{q .TokenManagerClientSecret}}
`))
