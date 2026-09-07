package commands

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var appsCmd = &cobra.Command{
	Use:   "apps",
	Short: "Manage deployed apps",
}

var AppsCmd = appsCmd

var deployCmd = &cobra.Command{
	Use:   "deploy <git-url>",
	Short: "Deploy a compose stack from a git repo",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeploy,
}

var (
	deployName string
	deployRef  string
)

func init() {
	deployCmd.Flags().StringVarP(&deployName, "name", "n", "", "app name (slug); derived from repo if empty")
	deployCmd.Flags().StringVarP(&deployRef, "ref", "r", "main", "branch or tag to deploy")
	appsCmd.AddCommand(deployCmd, appsListCmd, appsLogsCmd, appsRemoveCmd, appsRestartCmd, appsRedeployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	repoURL := args[0]
	if deployName == "" {
		deployName = nameFromURL(repoURL)
		if deployName == "" {
			return fmt.Errorf("could not derive app name from %q; pass --name", repoURL)
		}
	}

	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	cli, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer cli.Close()
	rt := composer.NewRuntime(cli, cfg.AppsRoot).WithLimits(cfg.DefaultMemoryBytes, cfg.DefaultPidsLimit)

	ctx := cmd.Context()

	host, _ := url.Parse(repoURL)
	auth := &builder.Auth{}
	if cred, err := st.GetCredentialForHost(ctx, host.Host); err == nil {
		auth.Username = cred.Username
		auth.Token = cred.Token
	}

	prev, _ := st.GetAppByName(ctx, deployName)

	sourceDir := filepath.Join(cfg.AppsRoot, "sources", nameOrUUID(prev, deployName))
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		return err
	}

	fmt.Println("Cloning repository...")
	if err := builder.Clone(ctx, repoURL, deployRef, sourceDir, auth); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}

	composePath, err := builder.FindComposeFile(sourceDir)
	if err != nil {
		return err
	}
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	spec, err := composer.Parse(composeBytes)
	if err != nil {
		return fmt.Errorf("compose parse: %w", err)
	}

	app := &store.App{
		ID:          nameOrUUID(prev, deployName),
		Name:        deployName,
		SourceType:  "git",
		SourceRef:   repoURL,
		GitRef:      deployRef,
		ComposeYAML: string(composeBytes),
		Env:         envOr(prev),
		Status:      "pending",
	}

	meta := composer.AppMeta{ID: app.ID, Name: app.Name, Env: app.Env, Label: app.Name}

	if prev != nil {
		fmt.Println("Tearing down previous deployment...")
		if oldSpec, err := composer.Parse([]byte(prev.ComposeYAML)); err == nil {
			if err := rt.Remove(ctx, meta, oldSpec); err != nil {
				fmt.Fprintf(os.Stderr, "warn: remove old: %v\n", err)
			}
		}
	}

	if prev == nil {
		app.ID = uuid.NewString()
		meta.ID = app.ID
		if err := st.CreateApp(ctx, app); err != nil {
			return err
		}
	} else {
		app.ID = prev.ID
		if err := st.UpdateApp(ctx, app); err != nil {
			return err
		}
	}

	fmt.Printf("Deploying %s (%s)...\n", app.Name, app.SourceRef)
	if err := rt.Deploy(ctx, meta, spec, sourceDir); err != nil {
		_ = st.UpdateAppStatus(ctx, app.ID, "error")
		return err
	}
	if err := st.UpdateAppStatus(ctx, app.ID, "running"); err != nil {
		return err
	}

	// Regenerate Traefik dynamic config (subdomain + auto path-prefix).
	domains, _ := st.GetAppDomains(ctx, app.ID)
	w := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	if _, err := traefik.ApplyAppRoute(ctx, traefik.AppOptions{
		Writer:          w,
		Client:          cli,
		AppName:         app.Name,
		Spec:            spec,
		Domains:         domains,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: traefik: %v\n", err)
	} else if len(domains) > 0 || cfg.AppPathPrefix != "" {
		publicURL := publicAppURL(cfg.PublicHost, cfg.AppPathPrefix, app.Name)
		fmt.Printf("Traefik config written; reachable at: %s\n", publicURL)
		if len(domains) > 0 {
			fmt.Printf("Custom domains: %s\n", strings.Join(domains, ", "))
		}
	}

	fmt.Printf("OK: app %s is running.\n", app.Name)
	return nil
}

func publicAppURL(host, prefix, appName string) string {
	if host == "" || prefix == "" {
		return ""
	}
	return fmt.Sprintf("https://%s%s%s/", host, prefix, appName)
}

func nameOrUUID(prev *store.App, _ string) string {
	if prev != nil {
		return prev.ID
	}
	return uuid.NewString()
}

func envOr(prev *store.App) map[string]string {
	if prev != nil && prev.Env != nil {
		return prev.Env
	}
	return map[string]string{}
}

func nameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := filepath.Base(u.Path)
	for _, ext := range []string{".git", ".git/"} {
		if len(p) >= len(ext) && p[len(p)-len(ext):] == ext {
			p = p[:len(p)-len(ext)]
		}
	}
	return p
}