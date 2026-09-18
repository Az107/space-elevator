package commands

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/deployer"
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
	deployName    string
	deployRef     string
	deployEnv     []string
	deploySecrets []string
	deployImage   string
	deployBuild   string
	deployRun     string
	deployPort    string
	deployKind    string
	deployLang    string
	deployRuntime string
	deployEntry   string
	deployS2Z     bool
	deployIdle    int
)

func init() {
	deployCmd.Flags().StringVarP(&deployName, "name", "n", "", "app name (slug); derived from repo if empty")
	deployCmd.Flags().StringVarP(&deployRef, "ref", "r", "main", "branch or tag to deploy")
	deployCmd.Flags().StringArrayVar(&deployEnv, "env", nil, "environment variable KEY=VALUE (repeatable; overrides the app's stored vars)")
	deployCmd.Flags().StringArrayVar(&deploySecrets, "secret", nil, "secret KEY=VALUE (repeatable; upserted alongside stored secrets)")
	deployCmd.Flags().StringVar(&deployImage, "image", "", "advanced deploy: builder image, e.g. node:20-bookworm")
	deployCmd.Flags().StringVar(&deployBuild, "build-cmd", "", "advanced deploy: build command run inside the image")
	deployCmd.Flags().StringVar(&deployRun, "run-cmd", "", "advanced deploy: run command (container CMD)")
	deployCmd.Flags().StringVar(&deployPort, "port", "", "advanced deploy: port the app listens on (default 8080)")
	deployCmd.Flags().StringVar(&deployKind, "kind", "web", "workload kind: web, function, or custom")
	deployCmd.Flags().StringVar(&deployLang, "language", "", "function: python or node")
	deployCmd.Flags().StringVar(&deployRuntime, "runtime", "", "function: base image version tag, e.g. 3.12 or 20")
	deployCmd.Flags().StringVar(&deployEntry, "entrypoint", "", "function: entrypoint file:handler, e.g. handler.py:handler")
	deployCmd.Flags().BoolVar(&deployS2Z, "scale-to-zero", false, "function: record scale-to-zero preference (activator pending)")
	deployCmd.Flags().IntVar(&deployIdle, "idle-timeout", 0, "function: idle timeout in seconds (activator pending)")
	appsCmd.AddCommand(deployCmd, uploadCmd, appsListCmd, appsLogsCmd, appsRemoveCmd, appsRestartCmd, appsRedeployCmd,
		appsStartCmd, appsStopCmd, appsRenameCmd)
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

	ctx, au := cliAudit(cmd.Context(), st)

	host, _ := url.Parse(repoURL)
	auth := &builder.Auth{}
	if cred, err := st.GetCredentialForHost(ctx, host.Host); err == nil {
		auth.Username = cred.Username
		auth.Token = cred.Token
	}

	prev, _ := st.GetAppByName(ctx, deployName)

	env, bad := store.ParseKVArgs(deployEnv)
	if len(bad) > 0 {
		return fmt.Errorf("invalid --env value(s) %q: use KEY=VALUE, key must match [A-Za-z_][A-Za-z0-9_]*", bad)
	}
	secrets, bad := store.ParseKVArgs(deploySecrets)
	if len(bad) > 0 {
		return fmt.Errorf("invalid --secret value(s) %q: use KEY=VALUE, key must match [A-Za-z_][A-Za-z0-9_]*", bad)
	}

	kind := strings.TrimSpace(deployKind)
	if kind == "" {
		kind = store.KindWeb
	}
	switch kind {
	case store.KindWeb, store.KindFunction, store.KindCustom:
	default:
		return fmt.Errorf("unknown --kind %q (use web, function, or custom)", kind)
	}
	fb := builder.FunctionBuild{Language: deployLang, Version: deployRuntime, Entrypoint: deployEntry}
	if kind == store.KindFunction {
		if err := fb.Validate(); err != nil {
			return fmt.Errorf("function: %w", err)
		}
	}

	// Advanced deploy: any build setting switches to custom build
	// mode. Re-deploying over an existing custom app inherits its
	// stored settings unless overridden by flags. Functions never use
	// advanced settings.
	image := strings.TrimSpace(deployImage)
	buildCmd := strings.TrimSpace(deployBuild)
	runCmd := strings.TrimSpace(deployRun)
	if prev != nil && prev.BuildMode == store.BuildModeCustom {
		if image == "" {
			image = prev.BuilderImage
		}
		if buildCmd == "" {
			buildCmd = prev.BuildCommand
		}
		if runCmd == "" {
			runCmd = prev.RunCommand
		}
	}
	custom := kind != store.KindFunction && (image != "" || runCmd != "" || buildCmd != "")
	port := 0
	if deployPort != "" || (custom && prev != nil && prev.BuildMode == store.BuildModeCustom && deployPort == "") {
		p, err := builder.ParsePort(deployPort)
		if err != nil {
			return err
		}
		port = p
	}
	if custom {
		cb := builder.CustomBuild{BuilderImage: image, BuildCommand: buildCmd, RunCommand: runCmd, ListenPort: port}
		if err := cb.Validate(); err != nil {
			return fmt.Errorf("advanced deploy: %w", err)
		}
		port = cb.Port()
	} else if deployPort != "" && kind != store.KindFunction {
		return fmt.Errorf("advanced deploy: --port needs --image or --run-cmd")
	}

	var buildReq *builder.CustomBuild
	if custom {
		buildReq = &builder.CustomBuild{BuilderImage: image, BuildCommand: buildCmd, RunCommand: runCmd, ListenPort: port}
	}

	tx := traefik.NewWriter(cfg.TraefikDir, cfg.CertResolver)
	dep := deployer.New(st, rt, cli, tx, deployer.Options{
		AppsRoot:        cfg.AppsRoot,
		PublicHost:      cfg.PublicHost,
		AppPathPrefix:   cfg.AppPathPrefix,
		RootlessGateway: cfg.RootlessGateway,
		CertResolver:    cfg.CertResolver,
		Audit:           au,
		Log: func(f string, a ...any) {
			fmt.Printf(f+"\n", a...)
		},
	})
	app, err := dep.DeployGit(ctx, deployer.GitRequest{
		Name:           deployName,
		RepoURL:        repoURL,
		Ref:            deployRef,
		Env:            env,
		Secrets:        secrets,
		Build:          buildReq,
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		ScaleToZero:    deployS2Z,
		IdleTimeout:    deployIdle,
	})
	if err != nil {
		return err
	}

	if cfg.PublicHost != "" && cfg.AppPathPrefix != "" {
		fmt.Printf("Traefik config written; reachable at: %s\n", publicAppURL(cfg.PublicHost, cfg.AppPathPrefix, app.Name))
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
