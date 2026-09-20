package commands

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/service"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var DoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that this host is correctly set up for space-elevator",
	Long: "Runs a series of diagnostics (Podman socket, config, Traefik, state DB,\n" +
		"dashboard reachability, systemd unit) and prints remediation hints.\n" +
		"Exits non-zero when a hard failure is found.",
	RunE: runDoctor,
}

type check struct {
	name   string
	status string // ok | warn | fail | skip | info
	detail string
}

func (c check) print() {
	label := map[string]string{
		"ok": "OK", "warn": "WARN", "fail": "FAIL", "skip": "SKIP", "info": "INFO",
	}[c.status]
	fmt.Printf("[%-4s] %-28s %s\n", label, c.name, c.detail)
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cfg := config.Default()
	path := config.ConfigPath()

	var checks []check
	add := func(c check) { checks = append(checks, c) }

	// Config file + validation.
	if _, err := os.Stat(path); err == nil {
		add(check{"config file", "ok", path})
	} else {
		add(check{"config file", "warn", "absent; using defaults + env (run `space-elevator setup`)"})
	}
	for _, i := range cfg.Validate() {
		st := "warn"
		if i.Level == "error" {
			st = "fail"
		}
		add(check{"config " + i.Level, st, i.Message})
	}

	// Directories / state DB.
	add(writableCheck("state dir", cfg.StateDir))
	add(writableCheck("apps root", cfg.AppsRoot))
	if st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db")); err != nil {
		add(check{"state db", "fail", err.Error()})
	} else {
		n, _ := st.CountUsers(ctx)
		if n == 0 {
			add(check{"admin account", "warn", "no account yet; open the dashboard to run /setup"})
		} else {
			add(check{"admin account", "ok", fmt.Sprintf("%d account(s)", n)})
		}
		st.Close()
	}

	// Podman socket + ping.
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		add(check{"podman socket", "fail", fmt.Sprintf("%s not found (enable: systemctl --user enable --now podman.socket)", cfg.SocketPath)})
	} else {
		cli, err := podman.New(cfg.SocketPath)
		if err != nil {
			add(check{"podman socket", "fail", err.Error()})
		} else {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, perr := cli.Ping(pingCtx)
			cancel()
			cli.Close()
			if perr != nil {
				add(check{"podman socket", "fail", fmt.Sprintf("cannot ping: %v", perr)})
			} else {
				add(check{"podman socket", "ok", cfg.SocketPath})
			}
		}
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command("systemctl", "--user", "is-enabled", "podman.socket").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "enabled" {
			add(check{"podman.socket unit", "warn", "not enabled for this user (systemctl --user enable --now podman.socket)"})
		} else {
			add(check{"podman.socket unit", "ok", "enabled"})
		}
	}

	// Traefik integration.
	if cfg.TraefikDir == "" {
		add(check{"traefik", "warn", "disabled; apps are reachable only via published host ports"})
	} else {
		add(writableCheck("traefik dir", cfg.TraefikDir))
		self := filepath.Join(cfg.TraefikDir, traefik.SelfRouteFileName)
		if b, err := os.ReadFile(self); err == nil {
			var v any
			if yerr := yaml.Unmarshal(b, &v); yerr != nil {
				add(check{"self-route", "fail", fmt.Sprintf("%s is not valid YAML: %v", self, yerr)})
			} else {
				add(check{"self-route", "ok", self})
			}
		} else {
			add(check{"self-route", "warn", fmt.Sprintf("%s not present (public_host=%q)", self, cfg.PublicHost)})
		}
		if cfg.CertResolver == "" {
			add(check{"cert resolver", "warn", "empty; routes will not request a certificate"})
		} else {
			add(check{"cert resolver", "ok", cfg.CertResolver})
		}
	}

	// Dashboard reachability on the local bind address.
	if u := localProbeURL(cfg.BindAddr); u != "" {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(u + "/login")
		if err != nil {
			add(check{"dashboard reachable", "warn", fmt.Sprintf("%s: %v (is the service running?)", u, err)})
		} else {
			resp.Body.Close()
			add(check{"dashboard reachable", "ok", fmt.Sprintf("%s → %d", u, resp.StatusCode)})
		}
	}

	// Public host DNS.
	if cfg.PublicHost != "" {
		if _, err := net.LookupHost(cfg.PublicHost); err != nil {
			add(check{"public host DNS", "warn", fmt.Sprintf("%s does not resolve: %v", cfg.PublicHost, err)})
		} else {
			add(check{"public host DNS", "ok", cfg.PublicHost})
		}
	}

	// systemd unit.
	if err := service.Available(); err != nil {
		add(check{"systemd service", "skip", err.Error()})
	} else {
		s := service.Query()
		switch {
		case !s.Installed:
			add(check{"systemd service", "warn", "not installed (space-elevator service install)"})
		case s.Active && s.Enabled:
			add(check{"systemd service", "ok", "active and enabled"})
		default:
			add(check{"systemd service", "warn", fmt.Sprintf("installed (active=%t enabled=%t)", s.Active, s.Enabled)})
		}
	}

	fmt.Println("space-elevator doctor")
	fmt.Println("=====================")
	fails := 0
	for _, c := range checks {
		c.print()
		if c.status == "fail" {
			fails++
		}
	}
	fmt.Println()
	if fails > 0 {
		return fmt.Errorf("%d check(s) failed", fails)
	}
	fmt.Println("No hard failures found.")
	return nil
}

func writableCheck(name, dir string) check {
	if dir == "" {
		return check{name, "skip", "not configured"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return check{name, "fail", fmt.Sprintf("%s: %v", dir, err)}
	}
	f, err := os.CreateTemp(dir, ".se-doctor-*")
	if err != nil {
		return check{name, "fail", fmt.Sprintf("%s: not writable: %v", dir, err)}
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)
	return check{name, "ok", dir}
}

// localProbeURL converts a bind address into a loopback URL for a health probe.
func localProbeURL(bindAddr string) string {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil || port == "" {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
