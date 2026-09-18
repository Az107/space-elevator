package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
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

	ctx, au := cliAudit(cmd.Context(), st)

	app, err := st.GetAppByName(ctx, args[0])
	if err != nil {
		return err
	}

	// A failed deploy may have empty or invalid stored compose. Removal
	// must still work: runtime teardown filters containers by label,
	// so only the image-cleanup step needs the spec.
	spec, err := composer.Parse([]byte(app.ComposeYAML))
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: stored compose is invalid (%v); removing by labels\n", err)
		spec = &composer.Spec{}
	}

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)

	meta := composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}
	fmt.Printf("Removing %s...\n", app.Name)
	if err := rt.Remove(ctx, meta, spec); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: remove runtime: %v\n", err)
	}
	if err := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver).Remove(app.Slug); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik remove: %v\n", err)
	}
	// Drop the local image so it can't be reused as a back-door after
	// the app is gone. Ignore errors — the image may already be gone.
	for svcName := range spec.Services {
		tag := rt.ImageTag(app.Slug, svcName)
		if err := cli.RemoveImage(ctx, tag, false); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: image remove %s: %v\n", tag, err)
		}
	}
	if err := store.DeleteAppArtifacts(cfg.AppsRoot, app); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: remove artifacts: %v\n", err)
	}
	if err := st.DeleteApp(ctx, app.ID); err != nil {
		return err
	}
	au.Record(ctx, audit.Event{
		Action:     audit.ActionAppRemove,
		TargetType: "app",
		TargetID:   app.ID,
		TargetName: app.Name,
		Outcome:    audit.OutcomeSuccess,
		Detail:     "source " + app.SourceType,
	})
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

var appsStartCmd = &cobra.Command{
	Use:   "start <app>",
	Short: "Start an app's containers",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsStart,
}

var appsStopCmd = &cobra.Command{
	Use:   "stop <app>",
	Short: "Stop an app's containers (keeps them for a later start)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsStop,
}

var appsRenameCmd = &cobra.Command{
	Use:   "rename <app> <new-name>",
	Short: "Rename an app (display name only; no rebuild)",
	Args:  cobra.ExactArgs(2),
	RunE:  runAppsRename,
}

// runAppsStartStop is shared by start and stop; start selects which.
func runAppsStartStop(cmd *cobra.Command, name string, start bool) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, au := cliAudit(cmd.Context(), st)

	app, err := st.GetAppByName(ctx, name)
	if err != nil {
		return err
	}
	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)
	tw := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	dep := deployer.New(st, rt, cli, tw, deployer.Options{
		AppsRoot:        cfg.AppsRoot,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
		CertResolver:    cfg.CertResolver,
	})
	meta := composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}

	if start {
		if err := rt.Start(ctx, meta); err != nil {
			return err
		}
		_ = st.UpdateAppStatus(ctx, app.ID, "running")
		if err := dep.SyncRoute(ctx, app); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik route: %v\n", err)
		}
		au.Record(ctx, audit.Event{Action: audit.ActionAppStart, TargetType: "app", TargetID: app.ID, TargetName: app.Name, Outcome: audit.OutcomeSuccess})
		fmt.Printf("OK: %s started.\n", app.Name)
		return nil
	}
	if err := rt.Stop(ctx, meta); err != nil {
		return err
	}
	_ = st.UpdateAppStatus(ctx, app.ID, "stopped")
	if err := dep.SyncRoute(ctx, app); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik route: %v\n", err)
	}
	au.Record(ctx, audit.Event{Action: audit.ActionAppStop, TargetType: "app", TargetID: app.ID, TargetName: app.Name, Outcome: audit.OutcomeSuccess})
	fmt.Printf("OK: %s stopped.\n", app.Name)
	return nil
}

func runAppsStart(cmd *cobra.Command, args []string) error {
	return runAppsStartStop(cmd, args[0], true)
}

func runAppsStop(cmd *cobra.Command, args []string) error {
	return runAppsStartStop(cmd, args[0], false)
}

func runAppsRename(cmd *cobra.Command, args []string) error {
	oldName := strings.ToLower(strings.TrimSpace(args[0]))
	newName := strings.ToLower(strings.TrimSpace(args[1]))
	if !deployer.ValidAppName(newName) {
		return fmt.Errorf("invalid app name %q: use 1-63 lowercase letters, digits, or hyphens", newName)
	}
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, au := cliAudit(cmd.Context(), st)

	app, err := st.GetAppByName(ctx, oldName)
	if err != nil {
		return err
	}
	if newName == app.Name {
		fmt.Printf("OK: %s already named that.\n", app.Name)
		return nil
	}
	if existing, err := st.GetAppByName(ctx, newName); err == nil && existing != nil {
		return fmt.Errorf("app %q already exists", newName)
	}
	if err := st.UpdateAppName(ctx, app.ID, newName); err != nil {
		return err
	}
	au.Record(ctx, audit.Event{Action: audit.ActionAppRename, TargetType: "app", TargetID: app.ID, TargetName: newName, Outcome: audit.OutcomeSuccess, Detail: "was " + app.Name})
	fmt.Printf("OK: %s renamed to %s (runtime unchanged).\n", app.Name, newName)
	return nil
}

func runAppsRestart(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, au := cliAudit(cmd.Context(), st)

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()

	app, err := st.GetAppByName(ctx, args[0])
	if err != nil {
		return err
	}
	if _, err := composer.Parse([]byte(app.ComposeYAML)); err != nil {
		return err
	}

	cs, err := cli.ListContainersFiltered(ctx, true, map[string][]string{
		"label": {composer.LabelApp + "=" + app.Slug},
	})
	if err != nil {
		return err
	}
	for _, c := range cs {
		full, err := cli.LookupID(context.Background(), c.ID)
		if err != nil || full == "" {
			continue
		}
		if err := cli.StopContainer(ctx, full, 10); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: stop %s: %v\n", c.ID, err)
		}
		if err := cli.StartContainer(ctx, full); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: start %s: %v\n", c.ID, err)
		}
		fmt.Printf("restarted %s\n", c.Name)
	}
	_ = st.UpdateAppStatus(ctx, app.ID, "running")
	tw := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)
	dep := deployer.New(st, rt, cli, tw, deployer.Options{
		AppsRoot:        cfg.AppsRoot,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
		CertResolver:    cfg.CertResolver,
	})
	if err := dep.SyncRoute(ctx, app); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: traefik route: %v\n", err)
	}
	au.Record(ctx, audit.Event{Action: audit.ActionAppRestart, TargetType: "app", TargetID: app.ID, TargetName: app.Name, Outcome: audit.OutcomeSuccess})
	return nil
}

func runAppsRedeploy(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, au := cliAudit(cmd.Context(), st)

	app, err := st.GetAppByName(ctx, args[0])
	if err != nil {
		return err
	}

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)

	dep := deployer.New(st, rt, cli, traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver), deployer.Options{
		AppsRoot:        cfg.AppsRoot,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
		CertResolver:    cfg.CertResolver,
		Audit:           au,
		Log: func(f string, a ...any) {
			fmt.Printf(f+"\n", a...)
		},
	})
	if err := dep.Redeploy(ctx, app); err != nil {
		return err
	}
	fmt.Printf("OK: %s redeployed.\n", app.Name)
	return nil
}
