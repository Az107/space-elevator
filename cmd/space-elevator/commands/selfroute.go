package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var selfRouteCmd = &cobra.Command{
	Use:   "self-route",
	Short: "Manage the dashboard's Traefik route (no DNS work required)",
}

var SelfRouteCmd = selfRouteCmd

var (
	selfRouteHost       string
	selfRoutePath       string
	selfRouteBackend    string
	selfRouteCert       string
)

func init() {
	f := selfRouteCmd.PersistentFlags()
	f.StringVar(&selfRouteHost, "host", "", "public hostname (defaults to SPACE_ELEVATOR_PUBLIC_HOST / elevator.albruiz.dev)")
	f.StringVar(&selfRoutePath, "path", "", "optional path prefix (defaults to SPACE_ELEVATOR_PUBLIC_PATH)")
	f.StringVar(&selfRouteBackend, "backend-url", "", "dashboard backend URL (defaults to SPACE_ELEVATOR_DASHBOARD_URL)")
	f.StringVar(&selfRouteCert, "cert-resolver", "", "cert resolver (defaults to SPACE_ELEVATOR_CERT_RESOLVER / letsencrypt)")

	selfRouteCmd.AddCommand(selfRouteRegisterCmd, selfRouteUnregisterCmd, selfRouteShowCmd)
}

func resolveSelfRoute() (traefik.SelfRouteConfig, *config.Config) {
	cfg := config.Default()
	c := traefik.SelfRouteConfig{
		Host:         firstNonEmpty(selfRouteHost, cfg.PublicHost, "elevator.albruiz.dev"),
		PathPrefix:   firstNonEmpty(selfRoutePath, cfg.PublicPath),
		BackendURL:   firstNonEmpty(selfRouteBackend, cfg.DashboardURL, "http://host.containers.internal:8080"),
		CertResolver: firstNonEmpty(selfRouteCert, cfg.CertResolver, "letsencrypt"),
	}
	return c, cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var selfRouteRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Write the dashboard's Traefik dynamic file",
	RunE:  runSelfRouteRegister,
}

func runSelfRouteRegister(cmd *cobra.Command, _ []string) error {
	c, cfg := resolveSelfRoute()
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	if err := w.WriteSelf(c); err != nil {
		return fmt.Errorf("write self-route: %w", err)
	}
	fmt.Printf("OK: dashboard exposed at %s\n", c.SelfURL())
	fmt.Printf("Wrote %s/%s\n", cfg.TraefikDir, traefik.SelfRouteFileName)
	return nil
}

var selfRouteUnregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Remove the dashboard's Traefik dynamic file",
	RunE:  runSelfRouteUnregister,
}

func runSelfRouteUnregister(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	if err := w.RemoveSelf(); err != nil {
		return fmt.Errorf("remove self-route: %w", err)
	}
	fmt.Println("OK: dashboard Traefik file removed.")
	return nil
}

var selfRouteShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the dashboard's current Traefik configuration",
	RunE:  runSelfRouteShow,
}

func runSelfRouteShow(cmd *cobra.Command, _ []string) error {
	c, cfg := resolveSelfRoute()
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	body, err := w.Read(traefik.SelfRouteFileName)
	if err != nil {
		exists, _ := w.SelfExists()
		if !exists {
			fmt.Println("(no self-route file; would resolve to)")
			fmt.Printf("  host:        %s\n", c.Host)
			fmt.Printf("  path:        %s\n", c.PathPrefix)
			fmt.Printf("  backend:     %s\n", c.BackendURL)
			fmt.Printf("  public URL:  %s\n", c.SelfURL())
			return nil
		}
		return err
	}
	fmt.Print(string(body))
	return nil
}
