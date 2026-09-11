package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/albertoruiz/space-elevator/cmd/space-elevator/commands"
)

var rootCmd = &cobra.Command{
	Use:   "space-elevator",
	Short: "Podman-first deployment tool",
	Long:  "Single binary for deploying and managing containerized apps via Podman + Traefik.",
}

func main() {
	rootCmd.AddCommand(commands.PodmanCmd)
	rootCmd.AddCommand(commands.AppsCmd)
	rootCmd.AddCommand(commands.DomainCmd)
	rootCmd.AddCommand(commands.ServeCmd)
	rootCmd.AddCommand(commands.SelfRouteCmd)
	rootCmd.AddCommand(commands.RouteCmd)
	rootCmd.AddCommand(commands.UserCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}