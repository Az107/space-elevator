package commands

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
)

var appsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List deployed apps",
	RunE:  runAppsList,
}

func runAppsList(cmd *cobra.Command, _ []string) error {
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

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot)

	fmt.Printf("%-20s %-12s %-20s %s\n", "NAME", "STATUS", "SOURCE", "CREATED")
	for _, a := range apps {
		status := a.Status
		spec, err := composer.Parse([]byte(a.ComposeYAML))
		if err == nil {
			if s, _, _ := rt.Status(cmd.Context(), composer.AppMeta{ID: a.ID, Name: a.Slug, Label: a.Slug}, spec); s != "" {
				status = s
			}
		}
		fmt.Printf("%-20s %-12s %-20s %s\n", a.Name, status, truncate(a.SourceRef, 20), a.CreatedAt.Format("2006-01-02 15:04"))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

var appsLogsCmd = &cobra.Command{
	Use:   "logs <app> [service]",
	Short: "Show logs for an app's service(s)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAppsLogs,
}

var (
	logsFollow bool
	logsTail   string
)

func init() {
	appsLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "follow log output")
	appsLogsCmd.Flags().StringVarP(&logsTail, "tail", "n", "100", "number of lines to show")
}

func runAppsLogs(cmd *cobra.Command, args []string) error {
	name := args[0]
	service := ""
	if len(args) > 1 {
		service = args[1]
	}

	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	app, err := st.GetAppByName(cmd.Context(), name)
	if err != nil {
		return err
	}

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot)

	rc, err := rt.Logs(cmd.Context(), composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}, service, logsFollow, logsTail)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(cmd.OutOrStdout(), rc)
	return err
}
