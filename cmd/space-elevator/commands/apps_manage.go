package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
	"github.com/albertoruiz/space-elevator/internal/web"
)

var _ = fmt.Sprintf

var appsRemoveCmd = &cobra.Command{
	Use:   "remove <app>",
	Short: "Remove an app (stop + delete containers + remove network)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsRemove,
}

func runAppsRemove(cmd *cobra.Command, args []string) error {
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
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)

	meta := composer.AppMeta{ID: app.ID, Name: app.Name, Label: app.Name}
	fmt.Printf("Removing %s...\n", app.Name)
	if err := rt.Remove(cmd.Context(), meta, spec); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: remove runtime: %v\n", err)
	}
	if err := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver).Remove(app.Name); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik remove: %v\n", err)
	}
	// Drop the local image so it can't be reused as a back-door after
	// the app is gone. Ignore errors — the image may already be gone.
	for svcName := range spec.Services {
		tag := rt.ImageTag(app.Name, svcName)
		if err := cli.RemoveImage(cmd.Context(), tag, false); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: image remove %s: %v\n", tag, err)
		}
	}
	if err := store.DeleteAppArtifacts(cfg.AppsRoot, app); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: remove artifacts: %v\n", err)
	}
	if err := st.DeleteApp(cmd.Context(), app.ID); err != nil {
		return err
	}
	fmt.Printf("OK: %s removed.\n", app.Name)
	return nil
}

var appsRestartCmd = &cobra.Command{
	Use:   "restart <app>",
	Short: "Restart all containers of an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsRestart,
}

var appsRedeployCmd = &cobra.Command{
	Use:   "redeploy <app>",
	Short: "Tear down and re-deploy an app from its stored source (re-pulls / re-builds, rewrites Traefik route)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsRedeploy,
}

func runAppsRestart(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()

	app, err := st.GetAppByName(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	if _, err := composer.Parse([]byte(app.ComposeYAML)); err != nil {
		return err
	}

	cs, err := cli.ListContainersFiltered(cmd.Context(), true, map[string][]string{
		"label": {composer.LabelApp + "=" + app.Name},
	})
	if err != nil {
		return err
	}
	for _, c := range cs {
		full, err := cli.LookupID(context.Background(), c.ID)
		if err != nil || full == "" {
			continue
		}
		if err := cli.StopContainer(cmd.Context(), full, 10); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: stop %s: %v\n", c.ID, err)
		}
		if err := cli.StartContainer(cmd.Context(), full); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: start %s: %v\n", c.ID, err)
		}
		fmt.Printf("restarted %s\n", c.Name)
	}
	return nil
}

func runAppsRedeploy(cmd *cobra.Command, args []string) error {
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
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)

	sourceDir, err := redeploySourceDir(app)
	if err != nil {
		return err
	}

	// For drops, regenerate Dockerfile + nginx.conf from the extracted
	// source so a re-deploy picks up the latest synth logic (doc-root
	// detection, <base href> injection, perms via the build-context
	// tar) without requiring a re-upload of the tarball.
	if app.SourceType == "drop" && app.DropKind == "static" {
		// If the app has any custom domains attached, it's served at the
		// subdomain root — leave base href as "/" so absolute paths in
		// the HTML resolve against the subdomain. Otherwise the auto
		// path-prefix route applies, and the base href must match the
		// Traefik router's "<app>-<service>" key.
		domains, _ := st.GetAppDomains(cmd.Context(), app.ID)
		baseHref := "/"
		if len(domains) == 0 && cfg.AppPathPrefix != "" {
			baseHref = strings.TrimRight(cfg.AppPathPrefix, "/") + "/" + app.Name + "-web/"
		}
		if _, err := web.WriteStaticFiles(sourceDir, baseHref); err != nil {
			return fmt.Errorf("regenerate synth files: %w", err)
		}
	}

	meta := composer.AppMeta{
		ID:         app.ID,
		Name:       app.Name,
		Label:      app.Name,
		StaticDrop: app.SourceType == "drop" && app.DropKind == "static",
	}
	fmt.Printf("Tearing down previous deployment for %s...\n", app.Name)
	if err := rt.Remove(cmd.Context(), meta, spec); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: remove: %v\n", err)
	}
	fmt.Printf("Re-deploying %s...\n", app.Name)
	if err := rt.Deploy(cmd.Context(), meta, spec, sourceDir); err != nil {
		_ = st.UpdateAppStatus(cmd.Context(), app.ID, "error")
		return err
	}
	if err := st.UpdateAppStatus(cmd.Context(), app.ID, "running"); err != nil {
		return err
	}

	domains, _ := st.GetAppDomains(cmd.Context(), app.ID)
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	if _, err := traefik.ApplyAppRoute(cmd.Context(), traefik.AppOptions{
		Writer:          w,
		Client:          cli,
		AppName:         app.Name,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
	}); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik: %v\n", err)
	}
	fmt.Printf("OK: %s redeployed.\n", app.Name)
	return nil
}

// redeploySourceDir finds the on-disk source for an app, depending on whether
// it was deployed from a git clone or a tarball drop.
func redeploySourceDir(app *store.App) (string, error) {
	home, _ := os.UserHomeDir()
	switch app.SourceType {
	case "git":
		return filepath.Join(home, "apps", "sources", app.ID), nil
	case "drop":
		return filepath.Join(home, "apps", "drops", app.ID), nil
	default:
		return "", fmt.Errorf("unknown source type %q", app.SourceType)
	}
}