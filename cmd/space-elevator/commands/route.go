package commands

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var routeCmd = &cobra.Command{
	Use:   "route",
	Short: "Inspect and regenerate Traefik dynamic files",
}

var RouteCmd = routeCmd

func init() {
	routeCmd.AddCommand(routeRegenCmd)
}

var routeRegenCmd = &cobra.Command{
	Use:   "regenerate <app>",
	Short: "Rewrite the Traefik dynamic file for an app from its current container IPs and attached domains",
	Args:  cobra.ExactArgs(1),
	RunE:  runRouteRegen,
}

func runRouteRegen(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	app, err := st.GetAppByName(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	spec, err := composer.Parse([]byte(app.ComposeYAML))
	if err != nil {
		return fmt.Errorf("stored compose is invalid: %w", err)
	}

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()

	domains, _ := st.GetAppDomains(cmd.Context(), app.ID)
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	wrote, err := traefik.ApplyAppRoute(cmd.Context(), traefik.AppOptions{
		Writer:          w,
		Client:          cli,
		AppName:         app.Name,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
	})
	if err != nil {
		return err
	}
	if !wrote {
		fmt.Printf("OK: %s has no domains; Traefik file removed.\n", app.Name)
		return nil
	}
	fmt.Printf("OK: rewrote %s/%s.yml\n", cfg.TraefikDir, app.Name)
	return nil
}
