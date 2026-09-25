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
	deployName      string
	deployRef       string
	deployEnv       []string
	deploySecrets   []string
	deployImage     string
	deployBuild     string
	deployRun       string
	deployServe     string
	deployBuildMode string
	deployPort      string
	deployKind      string
	deployLang      string
	deployRuntime   string
	deployEntry     string
	deployS2Z       bool
	deployIdle      int
)

func init() {
	deployCmd.Flags().StringVarP(&deployName, "name", "n", "", "app name (slug); derived from repo if empty")
	deployCmd.Flags().StringVarP(&deployRef, "ref", "r", "main", "branch or tag to deploy")
	deployCmd.Flags().StringArrayVar(&deployEnv, "env", nil, "environment variable KEY=VALUE (repeatable; overrides the app's stored vars)")
	deployCmd.Flags().StringArrayVar(&deploySecrets, "secret", nil, "secret KEY=VALUE (repeatable; upserted alongside stored secrets)")
	deployCmd.Flags().StringVar(&deployImage, "image", "", "advanced deploy: builder image, e.g. node:20-bookworm")
	deployCmd.Flags().StringVar(&deployBuild, "build-cmd", "", "advanced deploy: build command run inside the image")
	deployCmd.Flags().StringVar(&deployRun, "run-cmd", "", "advanced deploy: run command (container CMD)")
	deployCmd.Flags().StringVar(&deployServe, "serve-path", "", "static build: repository-relative directory to serve with Nginx")
	deployCmd.Flags().StringVar(&deployBuildMode, "build-mode", "", "web build mode: compose, server, or static")
	deployCmd.Flags().StringVar(&deployPort, "port", "", "advanced/static deploy: port the app listens on")
	deployCmd.Flags().StringVar(&deployKind, "kind", "web", "workload kind: web, function, or custom")
	deployCmd.Flags().StringVar(&deployLang, "language", "", "function: python or node")
	deployCmd.Flags().StringVar(&deployRuntime, "runtime", "", "function: base image version tag, e.g. 3.12 or 20")
	deployCmd.Flags().StringVar(&deployEntry, "entrypoint", "", "function: entrypoint file:handler, e.g. handler.py:handler")
	deployCmd.Flags().BoolVar(&deployS2Z, "scale-to-zero", false, "function: record scale-to-zero preference (activator pending)")
	deployCmd.Flags().IntVar(&deployIdle, "idle-timeout", 0, "function: idle timeout in seconds (activator pending)")
	appsCmd.AddCommand(deployCmd, uploadCmd, appsListCmd, appsLogsCmd, appsRemoveCmd, appsRestartCmd, appsRedeployCmd,
		appsStartCmd, appsStopCmd, appsRenameCmd, appsUpdateCmd)
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

	// Build settings: --build-mode server keeps the existing custom image
	// and run-command behavior; static builds use the same build command but
	// serve a repository-relative path with Nginx.
	image := strings.TrimSpace(deployImage)
	buildCmd := strings.TrimSpace(deployBuild)
	runCmd := strings.TrimSpace(deployRun)
	servePath := strings.TrimSpace(deployServe)
	buildMode := strings.TrimSpace(deployBuildMode)
	if buildMode == "" && servePath != "" {
		buildMode = store.BuildModeStatic
	}
	if buildMode == "" && prev != nil && (prev.BuildMode == store.BuildModeCustom || prev.BuildMode == store.BuildModeStatic) {
		buildMode = prev.BuildMode
	}
	if buildMode == "" {
		buildMode = store.BuildModeCompose
	}
	if buildMode == "server" {
		buildMode = store.BuildModeCustom
	}
	if buildMode != store.BuildModeCompose && buildMode != store.BuildModeCustom && buildMode != store.BuildModeStatic {
		return fmt.Errorf("unknown --build-mode %q (use compose, server, or static)", buildMode)
	}
	if prev != nil && prev.BuildMode == buildMode {
		if image == "" {
			image = prev.BuilderImage
		}
		if buildCmd == "" {
			buildCmd = prev.BuildCommand
		}
		if runCmd == "" {
			runCmd = prev.RunCommand
		}
		if servePath == "" {
			servePath = prev.ServePath
		}
	}
	staticBuild := kind == store.KindWeb && buildMode == store.BuildModeStatic
	custom := kind != store.KindFunction && !staticBuild && (buildMode == store.BuildModeCustom || image != "" || runCmd != "" || buildCmd != "")
	port := 0
	if staticBuild {
		if image == "" {
			image = "node:20-bookworm"
		}
		if deployPort == "" && prev != nil && prev.BuildMode == store.BuildModeStatic && prev.ListenPort > 0 {
			port = prev.ListenPort
		} else if deployPort == "" {
			port = builder.DefaultStaticListenPort
		} else {
			port, err = builder.ParsePort(deployPort)
			if err != nil {
				return err
			}
		}
		sb := builder.StaticBuild{BuilderImage: image, BuildCommand: buildCmd, ServePath: servePath, ListenPort: port}
		if err := sb.Validate(); err != nil {
			return fmt.Errorf("static deploy: %w", err)
		}
	} else if custom {
		if deployPort != "" {
			port, err = builder.ParsePort(deployPort)
			if err != nil {
				return err
			}
		} else if prev != nil && prev.BuildMode == store.BuildModeCustom && prev.ListenPort > 0 {
			port = prev.ListenPort
		} else {
			port = builder.DefaultListenPort
		}
		cb := builder.CustomBuild{BuilderImage: image, BuildCommand: buildCmd, RunCommand: runCmd, ListenPort: port}
		if err := cb.Validate(); err != nil {
			return fmt.Errorf("advanced deploy: %w", err)
		}
		port = cb.Port()
	} else if deployPort != "" && kind != store.KindFunction {
		return fmt.Errorf("advanced deploy: --port needs --image, --run-cmd, or --build-mode static")
	}

	var buildReq *builder.CustomBuild
	var staticReq *builder.StaticBuild
	if custom {
		buildReq = &builder.CustomBuild{BuilderImage: image, BuildCommand: buildCmd, RunCommand: runCmd, ListenPort: port}
	} else if staticBuild {
		staticReq = &builder.StaticBuild{BuilderImage: image, BuildCommand: buildCmd, ServePath: servePath, ListenPort: port}
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
		StaticBuild:    staticReq,
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
		fmt.Printf("Traefik config written; reachable at: %s\n", publicAppURL(cfg.PublicHost, cfg.AppPathPrefix, app.Slug))
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
