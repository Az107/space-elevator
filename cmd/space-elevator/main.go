package main

import (
	"fmt"
	"os"

	"github.com/albertoruiz/space-elevator/cmd/space-elevator/commands"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "space-elevator",
	Short:   "Podman-first deployment tool",
	Long:    "Single binary for deploying and managing containerized apps via Podman + Traefik.",
	Version: version,
}

// version is overridden at build time with
// -ldflags "-X main.version=vX.Y.Z"; releases set it from the git tag.
var version = "dev"

var configFlag string

func main() {
	rootCmd.PersistentFlags().StringVar(&configFlag, "config", "",
		"path to config file (default ~/.config/space-elevator/config.yaml)")
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if configFlag != "" {
			return os.Setenv("SPACE_ELEVATOR_CONFIG", configFlag)
		}
		return nil
	}
	rootCmd.AddCommand(commands.PodmanCmd)
	rootCmd.AddCommand(commands.AppsCmd)
	rootCmd.AddCommand(commands.DomainCmd)
	rootCmd.AddCommand(commands.ServeCmd)
	rootCmd.AddCommand(commands.SelfRouteCmd)
	rootCmd.AddCommand(commands.RouteCmd)
	rootCmd.AddCommand(commands.UserCmd)
	rootCmd.AddCommand(commands.ConfigCmd)
	rootCmd.AddCommand(commands.SetupCmd)
	rootCmd.AddCommand(commands.DoctorCmd)
	rootCmd.AddCommand(commands.ServiceCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
