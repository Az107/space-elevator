package commands

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var appsUpdateCmd = &cobra.Command{
	Use:   "update <app>",
	Short: "Update an app from a new Git revision or uploaded archive while preserving its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppsUpdate,
}

var (
	updateRef       string
	updateArchive   string
	updateSourceRef string
	updateSecrets   []string
	updateYes       bool
)

func init() {
	appsUpdateCmd.Flags().StringVar(&updateRef, "ref", "", "Git branch, tag, or commit to update to (defaults to the stored ref)")
	appsUpdateCmd.Flags().StringVar(&updateArchive, "archive", "", "new source archive for an archive app")
	appsUpdateCmd.Flags().StringVar(&updateSourceRef, "source-name", "", "display name for the uploaded archive")
	appsUpdateCmd.Flags().StringArrayVar(&updateSecrets, "secret", nil, "secret KEY=VALUE to upsert before updating (repeatable)")
	appsUpdateCmd.Flags().BoolVar(&updateYes, "yes", false, "skip the update confirmation prompt (updates are synchronous in the CLI)")
}

func runAppsUpdate(cmd *cobra.Command, args []string) error {
	if updateRef != "" && updateArchive != "" {
		return fmt.Errorf("--ref and --archive cannot be used together")
	}
	secrets, bad := store.ParseKVArgs(updateSecrets)
	if len(bad) > 0 {
		return fmt.Errorf("invalid --secret value(s) %q: use KEY=VALUE, key must match [A-Za-z_][A-Za-z0-9_]*", bad)
	}

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
		AppsRoot: cfg.AppsRoot, BackupDir: cfg.BackupDir, PodmanSocket: cfg.SocketPath,
		PublicHost: cfg.PublicHost, AppPathPrefix: cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway, CertResolver: cfg.CertResolver, Audit: au,
		Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	})

	if !updateYes {
		fmt.Printf("Updating %s. The app will be briefly interrupted; persistent data will be backed up first.\n", app.Name)
		fmt.Print("Type UPDATE to continue: ")
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil && line == "" {
			return fmt.Errorf("confirmation required; use --yes for non-interactive use")
		}
		if strings.TrimSpace(line) != "UPDATE" {
			return fmt.Errorf("update cancelled")
		}
	}
	result, err := dep.Update(ctx, deployer.UpdateRequest{
		Name: app.Name, Ref: strings.TrimSpace(updateRef), ArchivePath: strings.TrimSpace(updateArchive),
		SourceRef: strings.TrimSpace(updateSourceRef), Secrets: secrets,
	})
	if err != nil {
		return err
	}
	if result.Release.GitCommit != "" {
		fmt.Printf("Release: %s (%s)\n", result.Release.ID, result.Release.GitCommit)
	}
	if result.BackupDir != "" {
		fmt.Printf("Backup: %s\n", result.BackupDir)
	}
	fmt.Printf("OK: %s updated; persistent data was preserved.\n", app.Name)
	return nil
}
