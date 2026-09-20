package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/service"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// SetupCmd is the interactive first-run wizard.
var SetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Guided first-run configuration",
	Long: "Detects Podman, asks how the dashboard should be exposed, writes a\n" +
		"documented config file, optionally creates the admin account, and\n" +
		"optionally installs the systemd user service.",
	RunE: runSetup,
}

var (
	setupYes           bool
	setupMode          string
	setupHost          string
	setupAddr          string
	setupTraefikDir    string
	setupCertResolver  string
	setupAppsRoot      string
	setupGateway       string
	setupNoUser        bool
	setupNoService     bool
	setupLinger        bool
	setupUsername      string
	setupPasswordStdin bool
	setupForce         bool
)

func init() {
	f := SetupCmd.Flags()
	f.BoolVar(&setupYes, "yes", false, "non-interactive: accept detected/default answers")
	f.StringVar(&setupMode, "mode", "", "exposure mode: traefik | direct | proxy")
	f.StringVar(&setupHost, "host", "", "public hostname (traefik/proxy modes)")
	f.StringVar(&setupAddr, "addr", "", "dashboard bind address (host:port)")
	f.StringVar(&setupTraefikDir, "traefik-dir", "", "Traefik dynamic config directory")
	f.StringVar(&setupCertResolver, "cert-resolver", "", "Traefik TLS cert resolver name")
	f.StringVar(&setupAppsRoot, "apps-root", "", "root directory for app drops/checkouts")
	f.StringVar(&setupGateway, "rootless-gateway", "", "IP rootful Traefik uses to reach rootless ports")
	f.BoolVar(&setupNoUser, "no-user", false, "do not create the admin account")
	f.BoolVar(&setupNoService, "no-service", false, "do not install the systemd user service")
	f.BoolVar(&setupLinger, "linger", false, "enable user lingering so the service starts at boot")
	f.StringVar(&setupUsername, "username", "admin", "admin account username")
	f.BoolVar(&setupPasswordStdin, "password-stdin", false, "read the admin password from stdin")
	f.BoolVar(&setupForce, "force", false, "overwrite an existing config file")
}

type setupResult struct {
	mode            string
	bindAddr        string
	publicHost      string
	traefikDir      string
	certResolver    string
	appsRoot        string
	rootlessGateway string
	createUser      bool
	username        string
	password        string
	installService  bool
	linger          bool
}

func runSetup(cmd *cobra.Command, _ []string) error {
	cfg := config.BuiltinDefaults()
	legacy := config.DetectLegacy()
	if legacy.Found {
		config.ApplyLegacy(cfg, legacy)
	}
	applySetupFlags(cmd, cfg)

	// Preflight: report Podman status and try to detect the rootless gateway.
	notes := preflight(cmd.Context(), cfg)
	for _, n := range notes {
		fmt.Fprintln(cmd.ErrOrStderr(), "  "+n)
	}

	res := setupResult{
		mode:            defaultMode(cfg, setupMode),
		bindAddr:        cfg.BindAddr,
		publicHost:      cfg.PublicHost,
		traefikDir:      cfg.TraefikDir,
		certResolver:    cfg.CertResolver,
		appsRoot:        cfg.AppsRoot,
		rootlessGateway: cfg.RootlessGateway,
		username:        setupUsername,
		linger:          setupLinger,
	}

	if setupYes {
		if setupPasswordStdin {
			pw, err := readPasswordStdin()
			if err != nil {
				return err
			}
			res.password = pw
		}
		res.createUser = !setupNoUser && res.password != ""
		res.installService = !setupNoService
		normalizeMode(&res)
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("stdin is not a terminal; re-run with --yes and flags (see `space-elevator setup --help`)")
		}
		if err := runSetupForm(&res); err != nil {
			return err
		}
	}

	// Persist config.
	out := res.applyTo(cfg)
	path := config.ConfigPath()
	if _, err := os.Stat(path); err == nil && !setupForce {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s already exists; overwriting (use --force to silence this)\n", path)
	}
	if err := out.Save(path); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", path)

	// Admin account.
	if res.createUser && res.password != "" {
		if err := createAdmin(cmd.Context(), out, res.username, res.password); err != nil {
			return err
		}
	}

	// systemd unit.
	if res.installService {
		if err := installServiceUnit(out, res.linger); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not install the service: %v\n", err)
			fmt.Fprintln(cmd.ErrOrStderr(), "      run the dashboard in the foreground with: space-elevator serve")
		}
	}

	printSetupSummary(cmd, out, res)
	return nil
}

func applySetupFlags(cmd *cobra.Command, cfg *config.Config) {
	set := func(name string, dst *string) {
		if cmd.Flags().Changed(name) {
			*dst = cmd.Flags().Lookup(name).Value.String()
		}
	}
	set("host", &cfg.PublicHost)
	set("addr", &cfg.BindAddr)
	set("traefik-dir", &cfg.TraefikDir)
	set("cert-resolver", &cfg.CertResolver)
	set("apps-root", &cfg.AppsRoot)
	set("rootless-gateway", &cfg.RootlessGateway)
}

func defaultMode(cfg *config.Config, flag string) string {
	if flag != "" {
		return flag
	}
	if cfg.TraefikDir != "" {
		return "traefik"
	}
	return "direct"
}

// normalizeMode clears fields that don't apply to the chosen mode.
func normalizeMode(r *setupResult) {
	switch r.mode {
	case "direct":
		r.traefikDir = ""
		r.publicHost = ""
		r.certResolver = ""
		r.rootlessGateway = ""
		if r.bindAddr == "" || strings.HasPrefix(r.bindAddr, "0.0.0.0") {
			r.bindAddr = "127.0.0.1:8080"
		}
	case "proxy":
		r.certResolver = ""
		if r.bindAddr == "" {
			r.bindAddr = "0.0.0.0:8080"
		}
	case "traefik":
		if r.certResolver == "" {
			r.certResolver = "letsencrypt"
		}
		if r.bindAddr == "" {
			r.bindAddr = "0.0.0.0:8080"
		}
	}
}

func (r setupResult) applyTo(cfg *config.Config) *config.Config {
	cfg.BindAddr = r.bindAddr
	cfg.PublicHost = r.publicHost
	cfg.TraefikDir = r.traefikDir
	cfg.CertResolver = r.certResolver
	cfg.AppsRoot = r.appsRoot
	cfg.RootlessGateway = r.rootlessGateway
	cfg.DashboardURL = "" // re-derive
	return cfg
}

func runSetupForm(r *setupResult) error {
	mode := r.mode
	createUser := false
	var password, confirm string

	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("How should the dashboard and apps be exposed?").
				Options(
					huh.NewOption("Traefik + automatic HTTPS (public domain)", "traefik"),
					huh.NewOption("Direct host ports (no proxy)", "direct"),
					huh.NewOption("Behind an existing proxy (plain HTTP)", "proxy"),
				).
				Value(&mode),
		),
		huh.NewGroup(
			huh.NewInput().Title("Public hostname").Placeholder("elevator.example.com").
				Value(&r.publicHost).
				Validate(func(s string) error {
					if mode == "direct" {
						return nil
					}
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("a public hostname is required for this mode")
					}
					return nil
				}),
			huh.NewInput().Title("Traefik TLS cert resolver").Placeholder("letsencrypt").
				Value(&r.certResolver),
			huh.NewInput().Title("Traefik dynamic config directory").
				Placeholder("/etc/traefik/dynamic").
				Value(&r.traefikDir),
			huh.NewInput().Title("Rootless gateway IP (rootful Traefik → host-published ports)").
				Placeholder("10.89.0.1").
				Value(&r.rootlessGateway),
		).WithHideFunc(func() bool { return mode != "traefik" }),
		huh.NewGroup(
			huh.NewInput().Title("Traefik dynamic config directory").
				Placeholder("/etc/traefik/dynamic").
				Value(&r.traefikDir),
		).WithHideFunc(func() bool { return mode != "proxy" }),
		huh.NewGroup(
			huh.NewInput().Title("Dashboard bind address").Value(&r.bindAddr).
				Validate(validateHostPort),
			huh.NewInput().Title("Apps root directory").Value(&r.appsRoot),
		),
		huh.NewGroup(
			huh.NewConfirm().Title("Create the admin account now?").
				Description("Lets you log in at first visit without using the browser /setup form.").
				Value(&createUser),
		),
		huh.NewGroup(
			huh.NewInput().Title("Admin username").Value(&r.username),
			huh.NewInput().Title("Admin password").EchoMode(huh.EchoModePassword).
				Value(&password).
				Validate(func(s string) error {
					if len(s) < 8 {
						return fmt.Errorf("at least 8 characters")
					}
					return nil
				}),
			huh.NewInput().Title("Confirm password").EchoMode(huh.EchoModePassword).
				Value(&confirm).
				Validate(func(s string) error {
					if s != password {
						return fmt.Errorf("passwords don't match")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return !createUser }),
	}

	if err := huh.NewForm(groups...).Run(); err != nil {
		return err
	}

	r.mode = mode
	r.createUser = createUser
	r.password = password
	normalizeMode(r)

	// Offer to install the service (only where systemd is usable).
	if service.Available() == nil {
		install := !setupNoService
		linger := setupLinger
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Install and start the systemd user service?").Value(&install),
			huh.NewConfirm().Title("Start it at boot even before login (enable linger)?").
				Value(&linger),
		)).Run(); err != nil {
			return err
		}
		r.installService = install
		r.linger = install && linger
	}
	return nil
}

func validateHostPort(s string) error {
	host, port, err := splitHostPortStrict(s)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if port == "" {
		return fmt.Errorf("missing port")
	}
	return nil
}

func splitHostPortStrict(s string) (string, string, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", fmt.Errorf("expected host:port")
	}
	return s[:i], s[i+1:], nil
}

func preflight(ctx context.Context, cfg *config.Config) []string {
	var notes []string
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		notes = append(notes, fmt.Sprintf("podman socket not found at %s (enable: systemctl --user enable --now podman.socket)", cfg.SocketPath))
		return notes
	}
	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		notes = append(notes, "podman client error: "+err.Error())
		return notes
	}
	defer cli.Close()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		notes = append(notes, "podman socket present but ping failed: "+err.Error())
		return notes
	}
	notes = append(notes, "podman reachable on "+cfg.SocketPath)
	if cfg.RootlessGateway == "" {
		for _, netName := range []string{"podman", cfg.DefaultNetwork} {
			if gw, err := cli.NetworkGateway(pctx, netName); err == nil && gw != "" {
				cfg.RootlessGateway = gw
				notes = append(notes, fmt.Sprintf("detected rootless gateway %s (network %q)", gw, netName))
				break
			}
		}
	}
	return notes
}

func readPasswordStdin() (string, error) {
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if len(pw) < 8 {
		return "", fmt.Errorf("password from stdin must be at least 8 characters")
	}
	return pw, nil
}

func createAdmin(ctx context.Context, cfg *config.Config, username, password string) error {
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		fmt.Fprintln(os.Stderr, "note: an admin account already exists; leaving it unchanged")
		return nil
	}
	if username == "" {
		username = "admin"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := &store.User{ID: uuid.NewString(), Username: username, PasswordHash: string(hash)}
	if err := st.CreateUser(ctx, u); err != nil {
		return err
	}
	fmt.Printf("Created admin account %q\n", username)
	return nil
}

func installServiceUnit(cfg *config.Config, linger bool) error {
	bin, err := service.SelfBinary()
	if err != nil {
		return err
	}
	envFile := ""
	if _, err := os.Stat(service.DefaultEnvFile()); err == nil {
		envFile = service.DefaultEnvFile()
	}
	path, err := service.Install(service.Options{
		BinPath: bin,
		Addr:    cfg.BindAddr,
		EnvFile: envFile,
	}, true)
	if err != nil {
		return err
	}
	fmt.Printf("Installed and started %s\n", path)
	if linger {
		if err := service.EnableLinger(); err != nil {
			fmt.Fprintf(os.Stderr, "warn: %v\n", err)
		} else {
			fmt.Println("Enabled lingering (service starts at boot)")
		}
	}
	return nil
}

func printSetupSummary(cmd *cobra.Command, cfg *config.Config, res setupResult) {
	fmt.Println()
	fmt.Println("Setup complete.")
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "  config file: %s\n", config.ConfigPath())
	fmt.Fprintf(w, "  dashboard:   http://%s\n", cfg.BindAddr)
	switch res.mode {
	case "traefik":
		scheme := "https"
		if cfg.CertResolver == "" {
			scheme = "http"
		}
		fmt.Fprintf(w, "  public:      %s://%s/\n", scheme, cfg.PublicHost)
		fmt.Fprintf(w, "  apps:        %s://%s%s<app>/\n", scheme, cfg.PublicHost, cfg.AppPathPrefix)
	case "proxy":
		fmt.Fprintf(w, "  public:      http://%s/ (via your proxy)\n", cfg.PublicHost)
	case "direct":
		fmt.Fprintln(w, "  apps:        reachable on their published host ports")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next: `space-elevator doctor` to verify, then deploy with")
	fmt.Fprintln(w, "      `space-elevator apps deploy <git-url> --name demo`.")
}
