package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show Podman + space-elevator info",
	RunE:  runInfo,
}

var infoJSON bool

func init() {
	infoCmd.Flags().BoolVar(&infoJSON, "json", false, "output as JSON")
}

func runInfo(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	client, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	ping, err := client.Ping(cmd.Context())
	if err != nil {
		return fmt.Errorf("ping podman: %w", err)
	}

	info := map[string]any{
		"podman_socket":    cfg.SocketPath,
		"podman_api":       ping.APIVersion,
		"os_type":          ping.OSType,
		"experimental":     ping.Experimental,
		"builder_version":  ping.BuilderVersion,
		"data_dir":         cfg.DataDir,
		"state_dir":        cfg.StateDir,
		"traefik_dir":      cfg.TraefikDir,
		"quadlet_dir":      cfg.QuadletDir,
		"apps_root":        cfg.AppsRoot,
	}

	if infoJSON {
		b, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Println("space-elevator info")
		fmt.Println("===================")
		for k, v := range info {
			fmt.Printf("  %-18s %v\n", k+":", v)
		}
	}
	return nil
}