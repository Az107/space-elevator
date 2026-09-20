package service

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed space-elevator.service.tmpl
var unitTemplate string

var tmpl = template.Must(template.New("unit").Parse(unitTemplate))

// UnitName is the systemd user unit name.
const UnitName = "space-elevator.service"

// Options describe how to render the unit.
type Options struct {
	// BinPath is the absolute path to the space-elevator binary.
	BinPath string
	// Addr is the serve bind address (host:port).
	Addr string
	// EnvFile, when set, is loaded by systemd via EnvironmentFile. A leading
	// "-" is added by the template so a missing file is not fatal.
	EnvFile string
}

// Render returns the unit file contents.
func Render(o Options) (string, error) {
	if o.BinPath == "" {
		return "", fmt.Errorf("binary path is required")
	}
	if o.Addr == "" {
		o.Addr = "127.0.0.1:8080"
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, o); err != nil {
		return "", err
	}
	return b.String(), nil
}

// UserUnitDir returns ~/.config/systemd/user (respecting XDG_CONFIG_HOME).
func UserUnitDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/root"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

// UnitPath is the absolute path of the installed unit file.
func UnitPath() string {
	return filepath.Join(UserUnitDir(), UnitName)
}

// DefaultEnvFile is the optional environment file the unit reads.
func DefaultEnvFile() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/root"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "space-elevator", "env")
}

// SelfBinary resolves the running executable to an absolute path.
func SelfBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Abs(p)
}

// Available reports whether `systemctl --user` is usable on this host.
func Available() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found; this host does not use systemd")
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return fmt.Errorf("XDG_RUNTIME_DIR is unset; no systemd user session (are you root? try again as your normal user)")
	}
	if out, err := systemctl("--user", "is-system-running"); err != nil {
		// Degraded is still usable; only fail when the bus is unreachable.
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "failed to connect") || strings.Contains(msg, "no medium found") {
			return fmt.Errorf("cannot reach the systemd user bus: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func systemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Install writes the unit, reloads systemd, and optionally enables + starts it.
func Install(o Options, enable bool) (string, error) {
	if err := Available(); err != nil {
		return "", err
	}
	body, err := Render(o)
	if err != nil {
		return "", err
	}
	dir := UserUnitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := UnitPath()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	if out, err := systemctl("--user", "daemon-reload"); err != nil {
		return path, fmt.Errorf("daemon-reload: %s: %w", strings.TrimSpace(out), err)
	}
	if enable {
		if out, err := systemctl("--user", "enable", "--now", UnitName); err != nil {
			return path, fmt.Errorf("enable --now: %s: %w", strings.TrimSpace(out), err)
		}
	}
	return path, nil
}

// Uninstall stops, disables and removes the unit. Data is left untouched.
func Uninstall() error {
	if err := Available(); err != nil {
		return err
	}
	// Best-effort stop/disable; a not-installed unit is fine.
	_, _ = systemctl("--user", "disable", "--now", UnitName)
	if err := os.Remove(UnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if out, err := systemctl("--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// Control runs a systemctl verb (start/stop/restart/enable/disable) and
// returns its combined output.
func Control(verb string) (string, error) {
	if err := Available(); err != nil {
		return "", err
	}
	return systemctl("--user", verb, UnitName)
}

// Status describes the unit's current state.
type Status struct {
	Installed bool
	Active    bool
	Enabled   bool
	Lingering bool
	Detail    string
}

// Query inspects the unit.
func Query() Status {
	var s Status
	if _, err := os.Stat(UnitPath()); err == nil {
		s.Installed = true
	}
	if out, err := systemctl("--user", "is-active", UnitName); err == nil {
		s.Active = true
	} else {
		s.Detail = strings.TrimSpace(out)
	}
	if out, err := systemctl("--user", "is-enabled", UnitName); err == nil {
		s.Enabled = strings.TrimSpace(out) == "enabled"
	}
	if out, err := exec.Command("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger").CombinedOutput(); err == nil {
		s.Lingering = strings.TrimSpace(string(out)) == "Linger=yes"
	}
	return s
}

// EnableLinger turns on user lingering so the service starts at boot without a
// login session. It needs a TTY for a possible polkit prompt; callers should
// treat failure as non-fatal.
func EnableLinger() error {
	user := os.Getenv("USER")
	if user == "" {
		return fmt.Errorf("USER is unset; cannot enable linger")
	}
	out, err := exec.Command("loginctl", "enable-linger", user).CombinedOutput()
	if err != nil {
		return fmt.Errorf("loginctl enable-linger %s: %s: %w", user, strings.TrimSpace(string(out)), err)
	}
	return nil
}
