package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/service"
)

var ServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Install and control the systemd user service",
	Long: "Manages the per-user systemd unit that runs `space-elevator serve`.\n" +
		"Linux + systemd only; on other platforms run `space-elevator serve` directly.",
}

var (
	serviceInstallNoEnable bool
	serviceInstallLinger   bool
	serviceLogsFollow      bool
	serviceLogsLines       int
)

func init() {
	serviceInstallCmd.Flags().BoolVar(&serviceInstallNoEnable, "no-enable", false, "write the unit but do not enable/start it")
	serviceInstallCmd.Flags().BoolVar(&serviceInstallLinger, "linger", false, "also run `loginctl enable-linger` so the service starts at boot")
	serviceLogsCmd.Flags().BoolVarP(&serviceLogsFollow, "follow", "f", false, "stream new log lines")
	serviceLogsCmd.Flags().IntVarP(&serviceLogsLines, "lines", "n", 100, "number of recent lines to show")
	ServiceCmd.AddCommand(
		serviceInstallCmd, serviceUninstallCmd,
		serviceStartCmd, serviceStopCmd, serviceRestartCmd,
		serviceEnableCmd, serviceDisableCmd,
		serviceStatusCmd, serviceLogsCmd,
	)
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the systemd user unit and enable it",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := config.Default()
		bin, err := service.SelfBinary()
		if err != nil {
			return err
		}
		envFile := ""
		if _, err := os.Stat(service.DefaultEnvFile()); err == nil {
			envFile = service.DefaultEnvFile()
		}
		path, err := service.Install(service.Options{
			BinPath: bin,
			Addr:    cfg.BindAddr,
			EnvFile: envFile,
		}, !serviceInstallNoEnable)
		if err != nil {
			return err
		}
		fmt.Printf("Installed %s\n", path)
		fmt.Printf("  ExecStart: %s serve --addr %s\n", bin, cfg.BindAddr)
		if envFile != "" {
			fmt.Printf("  EnvironmentFile: %s\n", envFile)
		}
		if !serviceInstallNoEnable {
			fmt.Println("  enabled and started")
		}
		if serviceInstallLinger {
			if err := service.EnableLinger(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: %v\n", err)
			} else {
				fmt.Println("  lingering enabled (starts at boot)")
			}
		}
		return nil
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop, disable and remove the unit (keeps your data)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Removed the space-elevator unit. Data and apps were left untouched.")
		return nil
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show unit install/active/enabled state",
	RunE: func(cmd *cobra.Command, _ []string) error {
		s := service.Query()
		fmt.Printf("unit file:  %s\n", service.UnitPath())
		fmt.Printf("installed:  %t\n", s.Installed)
		fmt.Printf("active:     %t\n", s.Active)
		fmt.Printf("enabled:    %t\n", s.Enabled)
		fmt.Printf("lingering:  %t\n", s.Lingering)
		if s.Detail != "" && !s.Active {
			fmt.Printf("detail:     %s\n", s.Detail)
		}
		return nil
	},
}

func serviceVerbCmd(use, short, verb string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := service.Control(verb)
			if err != nil {
				return fmt.Errorf("systemctl --user %s %s: %s: %w", verb, service.UnitName, strings.TrimSpace(out), err)
			}
			fmt.Printf("systemctl --user %s: %s\n", verb, strings.TrimSpace(out))
			return nil
		},
	}
}

var (
	serviceStartCmd   = serviceVerbCmd("start", "Start the service", "start")
	serviceStopCmd    = serviceVerbCmd("stop", "Stop the service", "stop")
	serviceRestartCmd = serviceVerbCmd("restart", "Restart the service", "restart")
	serviceEnableCmd  = serviceVerbCmd("enable", "Enable the service at login", "enable")
	serviceDisableCmd = serviceVerbCmd("disable", "Disable the service", "disable")
)

var serviceLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show service logs from journald",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := exec.LookPath("journalctl"); err != nil {
			return fmt.Errorf("journalctl not found")
		}
		args := []string{"--user", "-u", service.UnitName, "-n", fmt.Sprintf("%d", serviceLogsLines)}
		if serviceLogsFollow {
			args = append(args, "-f")
		}
		c := exec.Command("journalctl", args...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Stdin = os.Stdin
		return c.Run()
	},
}
