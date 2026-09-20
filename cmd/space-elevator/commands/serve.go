package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/traefik"
	"github.com/albertoruiz/space-elevator/internal/web"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the web dashboard",
	RunE:  runServe,
}

var ServeCmd = serveCmd

var (
	serveAddr      string
	serveAutoRoute bool
)

func init() {
	serveCmd.Flags().StringVarP(&serveAddr, "addr", "a", "127.0.0.1:8080", "bind address")
	serveCmd.Flags().BoolVar(&serveAutoRoute, "register-self", true, "write the dashboard's Traefik file on startup (unregister with 'self-route unregister')")
}

// auditRetention is how long audit events are kept before the hourly
// sweep prunes them.
const auditRetention = 90 * 24 * time.Hour

func runServe(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()

	// Safety net for upgrades from a pre-config-file install: neutral defaults
	// would otherwise stop writing routes that an existing host still needs.
	if _, err := os.Stat(config.ConfigPath()); os.IsNotExist(err) {
		if l := config.DetectLegacy(); l.Found && cfg.TraefikDir == "" {
			fmt.Fprintf(os.Stderr, "WARNING: no config file, but a Traefik setup was detected at %s\n", l.TraefikDir)
			fmt.Fprintln(os.Stderr, "         run `space-elevator config init --from-legacy` to preserve existing routes")
		}
	}

	// The config file/env bind address is the default; an explicit --addr wins.
	addr := serveAddr
	if !cmd.Flags().Changed("addr") && cfg.BindAddr != "" {
		addr = cfg.BindAddr
	}
	for _, i := range cfg.Validate() {
		fmt.Fprintf(os.Stderr, "config %s: %s\n", i.Level, i.Message)
	}

	srv, err := web.NewServer(cfg)
	if err != nil {
		return err
	}

	if serveAutoRoute && (cfg.PublicHost != "" || cfg.PublicPath != "") {
		w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
		c := traefik.SelfRouteConfig{
			Host:         cfg.PublicHost,
			PathPrefix:   cfg.PublicPath,
			BackendURL:   cfg.DashboardURL,
			CertResolver: cfg.CertResolver,
		}
		if err := w.WriteSelf(c); err != nil {
			fmt.Fprintf(os.Stderr, "warn: self-route: %v\n", err)
		} else {
			fmt.Printf("Dashboard route: %s -> %s\n", c.SelfURL(), cfg.DashboardURL)
			fmt.Printf("Traefik file:    %s/%s\n", cfg.TraefikDir, traefik.SelfRouteFileName)
		}
	} else if serveAutoRoute {
		fmt.Fprintln(os.Stderr, "warn: no public_host configured; skipping the dashboard Traefik route (run `space-elevator setup` or set SPACE_ELEVATOR_PUBLIC_HOST)")
	}

	h := srv.Routes()
	fmt.Printf("space-elevator listening on http://%s\n", addr)

	// Heal route/runtime drift left by a crash, host event, or proxy
	// cutover: stale routes (Traefik 502) and missing routes (404). Runs
	// in the background so the dashboard is reachable immediately.
	go srv.ReconcileRoutes(cmd.Context())

	srvErr := make(chan error, 1)
	go func() { srvErr <- http.ListenAndServe(addr, h) }()

	// Background GC: every hour, sweep orphan drop dirs / tarballs /
	// unused images plus expired sessions so a crashed upload doesn't
	// leave bytes (or dead auth rows) on disk.
	gcStop := make(chan struct{})
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-gcStop:
				return
			case <-t.C:
				runGcQuiet(cmd.Context(), cfg)
				purgeCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if err := srv.Store.PurgeExpiredSessions(purgeCtx); err != nil {
					fmt.Fprintf(os.Stderr, "gc: sessions: %v\n", err)
				}
				if _, err := srv.Store.PruneAudit(purgeCtx, time.Now().Add(-auditRetention)); err != nil {
					fmt.Fprintf(os.Stderr, "gc: audit: %v\n", err)
				}
				cancel()
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-srvErr:
		close(gcStop)
		return err
	case <-stop:
		fmt.Println("\nshutting down")
		close(gcStop)
		return nil
	}
}

// runGcQuiet is a thin wrapper that the background goroutine uses so
// periodic GC failures don't spam the foreground logs.
func runGcQuiet(ctx context.Context, cfg *config.Config) {
	_ = ctx
	cmd := &cobra.Command{}
	_ = cmd
	if err := runAppsGc(cmd, nil); err != nil {
		fmt.Fprintf(os.Stderr, "gc: %v\n", err)
	}
}
