package commands

import (
	"fmt"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/spf13/cobra"
)

var PodmanCmd = &cobra.Command{
	Use:   "podman",
	Short: "Interact directly with Podman",
}

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List running containers",
	RunE:  runPs,
}

var psAll bool

func init() {
	psCmd.Flags().BoolVarP(&psAll, "all", "a", false, "show all containers (default shows only running)")
	PodmanCmd.AddCommand(psCmd, imagesCmd, infoCmd, networksCmd)
}

func runPs(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	client, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	cs, err := client.ListContainers(cmd.Context(), psAll)
	if err != nil {
		return err
	}

	if len(cs) == 0 {
		fmt.Println("No containers found.")
		return nil
	}

	fmt.Printf("%-14s %-30s %-30s %-12s %s\n", "ID", "NAME", "IMAGE", "STATE", "STATUS")
	for _, ctr := range cs {
		fmt.Printf("%-14s %-30s %-30s %-12s %s\n", ctr.ID, ctr.Name, ctr.Image, ctr.State, ctr.Status)
	}
	return nil
}
