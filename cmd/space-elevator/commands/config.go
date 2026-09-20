package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/config"
)

var ConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and initialize the configuration file",
	Long: "space-elevator resolves configuration from (highest priority first):\n" +
		"CLI flags, SPACE_ELEVATOR_* environment variables, the YAML config file,\n" +
		"then built-in defaults. See docs/configuration.md for every key.",
}

var (
	configInitFromLegacy bool
	configInitForce      bool
	configInitPrint      bool
)

func init() {
	configInitCmd.Flags().BoolVar(&configInitFromLegacy, "from-legacy", false,
		"seed values detected from an existing Traefik dynamic directory")
	configInitCmd.Flags().BoolVar(&configInitForce, "force", false,
		"overwrite an existing config file")
	configInitCmd.Flags().BoolVar(&configInitPrint, "print", false,
		"print the file to stdout instead of writing it")
	ConfigCmd.AddCommand(configPathCmd, configShowCmd, configValidateCmd, configInitCmd)
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the active config file path",
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := config.ConfigPath()
		_, err := os.Stat(path)
		fmt.Println(path)
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.ErrOrStderr(), "(file does not exist yet; run `space-elevator setup` or `space-elevator config init`)")
		}
		return nil
	},
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a documented YAML config file",
	RunE:  runConfigInit,
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	path := config.ConfigPath()

	cfg := config.BuiltinDefaults()
	if configInitFromLegacy {
		l := config.DetectLegacy()
		if !l.Found {
			fmt.Fprintln(cmd.ErrOrStderr(), "note: no legacy Traefik directory detected; writing neutral defaults")
		} else {
			config.ApplyLegacy(cfg, l)
			fmt.Fprintf(cmd.ErrOrStderr(), "note: seeded from %s (public_host=%q cert_resolver=%q gateway=%q)\n",
				l.TraefikDir, cfg.PublicHost, cfg.CertResolver, cfg.RootlessGateway)
		}
	}
	// Re-derive dashboard URL for the seeded values.
	// (Save renders the effective values, including the derived URL.)

	if configInitPrint {
		return cfg.SaveTo(cmd.OutOrStdout())
	}
	if _, err := os.Stat(path); err == nil && !configInitForce {
		return fmt.Errorf("config file already exists: %s (use --force to overwrite)", path)
	}
	if err := cfg.Save(path); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", path)
	fmt.Println("Edit it or override with SPACE_ELEVATOR_* env vars; see docs/configuration.md.")
	return nil
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show effective configuration and where values come from",
	RunE:  runConfigShow,
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	path := config.ConfigPath()
	_, statErr := os.Stat(path)
	fileState := "present"
	if os.IsNotExist(statErr) {
		fileState = "absent (using defaults + env)"
	}

	fmt.Printf("config file: %s  [%s]\n\n", path, fileState)

	rows := [][2]string{
		{"bind_addr", cfg.BindAddr},
		{"socket_path", cfg.SocketPath},
		{"data_dir", cfg.DataDir},
		{"state_dir", cfg.StateDir},
		{"traefik_dir", cfg.TraefikDir},
		{"public_host", cfg.PublicHost},
		{"public_path", cfg.PublicPath},
		{"dashboard_url", cfg.DashboardURL},
		{"cert_resolver", cfg.CertResolver},
		{"quadlet_dir", cfg.QuadletDir},
		{"apps_root", cfg.AppsRoot},
		{"default_network", cfg.DefaultNetwork},
		{"app_path_prefix", cfg.AppPathPrefix},
		{"rootless_gateway", cfg.RootlessGateway},
		{"memory_limit", config.FormatSizeBytes(cfg.DefaultMemoryBytes)},
		{"pids_limit", fmt.Sprintf("%d", cfg.DefaultPidsLimit)},
		{"insecure_cookies", fmt.Sprintf("%t", cfg.InsecureCookies)},
	}
	for _, r := range rows {
		fmt.Printf("  %-18s %s\n", r[0]+":", r[1])
	}

	env := map[string]string{}
	for _, k := range config.EnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if len(env) > 0 {
		fmt.Println("\nenvironment overrides:")
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, env[k])
		}
	}
	return nil
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the effective configuration",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load("")
		if err != nil {
			return err
		}
		issues := cfg.Validate()
		if len(issues) == 0 {
			fmt.Println("OK: configuration is valid.")
			return nil
		}
		failed := false
		for _, i := range issues {
			fmt.Printf("%-7s %s\n", strings.ToUpper(i.Level)+":", i.Message)
			if i.Level == "error" {
				failed = true
			}
		}
		if failed {
			return fmt.Errorf("configuration has errors")
		}
		return nil
	},
}
