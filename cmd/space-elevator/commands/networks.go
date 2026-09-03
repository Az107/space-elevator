package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
)

var networksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List Podman networks",
	RunE:  runNetworks,
}

func runNetworks(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	client, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	nets, err := client.ListNetworks(cmd.Context())
	if err != nil {
		return err
	}

	fmt.Printf("%-14s %-25s %-12s %-12s %s\n", "ID", "NAME", "DRIVER", "SCOPE", "SUBNET")
	for _, n := range nets {
		fmt.Printf("%-14s %-25s %-12s %-12s %s\n", n.ID, n.Name, n.Driver, n.Scope, n.Subnet)
	}
	return nil
}