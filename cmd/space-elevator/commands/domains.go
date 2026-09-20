package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Manage domains routed to apps via Traefik",
}

var DomainCmd = domainCmd

var domainAddCmd = &cobra.Command{
	Use:   "add <app> <domain>",
	Short: "Attach a domain to an app (regenerates Traefik config)",
	Args:  cobra.ExactArgs(2),
	RunE:  runDomainAdd,
}

var domainRemoveCmd = &cobra.Command{
	Use:   "remove <app> <domain>",
	Short: "Remove a domain from an app",
	Args:  cobra.ExactArgs(2),
	RunE:  runDomainRemove,
}

var domainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all domains attached to apps",
	RunE:  runDomainList,
}

func init() {
	domainCmd.AddCommand(domainAddCmd, domainRemoveCmd, domainListCmd)
}

func runDomainAdd(cmd *cobra.Command, args []string) error {
	appName := args[0]
	domain := strings.ToLower(strings.TrimSpace(args[1]))

	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	app, err := st.GetAppByName(cmd.Context(), appName)
	if err != nil {
		return fmt.Errorf("app %q: %w", appName, err)
	}
	current, _ := st.GetAppDomains(cmd.Context(), app.ID)
	for _, d := range current {
		if d == domain {
			fmt.Printf("%s already attached.\n", domain)
			return nil
		}
	}
	updated := append(current, domain)
	if err := st.SetAppDomains(cmd.Context(), app.ID, updated); err != nil {
		return err
	}
	return regenerateTraefik(cmd, cfg, st, app)
}

func runDomainRemove(cmd *cobra.Command, args []string) error {
	appName := args[0]
	domain := strings.ToLower(strings.TrimSpace(args[1]))

	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	app, err := st.GetAppByName(cmd.Context(), appName)
	if err != nil {
		return fmt.Errorf("app %q: %w", appName, err)
	}
	current, err := st.GetAppDomains(cmd.Context(), app.ID)
	if err != nil {
		return err
	}
	updated := make([]string, 0, len(current))
	found := false
	for _, d := range current {
		if d == domain {
			found = true
			continue
		}
		updated = append(updated, d)
	}
	if !found {
		return fmt.Errorf("domain %q not attached to %q", domain, appName)
	}
	if err := st.SetAppDomains(cmd.Context(), app.ID, updated); err != nil {
		return err
	}
	return regenerateTraefik(cmd, cfg, st, app)
}

func runDomainList(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	apps, err := st.ListApps(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Printf("%-20s %s\n", "APP", "DOMAINS")
	for _, a := range apps {
		domains, _ := st.GetAppDomains(cmd.Context(), a.ID)
		if len(domains) == 0 {
			continue
		}
		fmt.Printf("%-20s %s\n", a.Name, strings.Join(domains, ", "))
	}
	return nil
}

func regenerateTraefik(cmd *cobra.Command, cfg *config.Config, st *store.Store, app *store.App) error {
	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()

	domains, err := st.GetAppDomains(cmd.Context(), app.ID)
	if err != nil {
		return err
	}
	spec, err := composer.Parse([]byte(app.ComposeYAML))
	if err != nil {
		return fmt.Errorf("stored compose is invalid: %w", err)
	}

	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	wrote, err := traefik.ApplyAppRoute(cmd.Context(), traefik.AppOptions{
		Writer:          w,
		Client:          cli,
		AppName:         app.Slug,
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

	parts := []string{}
	if len(domains) > 0 {
		parts = append(parts, "domains="+strings.Join(domains, ","))
	}
	if cfg.PublicHost != "" && cfg.AppPathPrefix != "" {
		parts = append(parts, "path=https://"+cfg.PublicHost+cfg.AppPathPrefix+app.Name+"/")
	}
	fmt.Printf("OK: app %s routes [%s]\n", app.Name, strings.Join(parts, " | "))
	return nil
}
