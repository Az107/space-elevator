package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/deployer"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/traefik"
)

var uploadCmd = &cobra.Command{
	Use:   "upload <archive.tar.gz|.zip>",
	Short: "Deploy an uploaded archive as a web app, function, or container",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpload,
}

var (
	uploadName    string
	uploadKind    string
	uploadLang    string
	uploadRuntime string
	uploadEntry   string
	uploadEnv     []string
	uploadSecrets []string
	uploadS2Z     bool
	uploadIdle    int
)

func init() {
	uploadCmd.Flags().StringVarP(&uploadName, "name", "n", "", "app name (slug); auto-generated (drop-…) if empty")
	uploadCmd.Flags().StringVar(&uploadKind, "kind", "web", "workload kind: web, function, or custom")
	uploadCmd.Flags().StringVar(&uploadLang, "language", "", "function: python or node")
	uploadCmd.Flags().StringVar(&uploadRuntime, "runtime", "", "function: base image version tag, e.g. 3.12 or 20")
	uploadCmd.Flags().StringVar(&uploadEntry, "entrypoint", "", "function: entrypoint file:handler, e.g. handler.py:handler")
	uploadCmd.Flags().StringArrayVar(&uploadEnv, "env", nil, "environment variable KEY=VALUE (repeatable)")
	uploadCmd.Flags().StringArrayVar(&uploadSecrets, "secret", nil, "secret KEY=VALUE (repeatable)")
	uploadCmd.Flags().BoolVar(&uploadS2Z, "scale-to-zero", false, "function: record scale-to-zero preference (activator pending)")
	uploadCmd.Flags().IntVar(&uploadIdle, "idle-timeout", 0, "function: idle timeout in seconds (activator pending)")
}

func runUpload(cmd *cobra.Command, args []string) error {
	archive := args[0]

	kind := strings.TrimSpace(uploadKind)
	if kind == "" {
		kind = store.KindWeb
	}
	switch kind {
	case store.KindWeb, store.KindFunction, store.KindCustom:
	default:
		return fmt.Errorf("unknown --kind %q (use web, function, or custom)", kind)
	}
	fb := builder.FunctionBuild{Language: uploadLang, Version: uploadRuntime, Entrypoint: uploadEntry}
	if kind == store.KindFunction {
		if err := fb.Validate(); err != nil {
			return fmt.Errorf("function: %w", err)
		}
	}

	env, bad := store.ParseKVArgs(uploadEnv)
	if len(bad) > 0 {
		return fmt.Errorf("invalid --env value(s) %q: use KEY=VALUE, key must match [A-Za-z_][A-Za-z0-9_]*", bad)
	}
	secrets, bad := store.ParseKVArgs(uploadSecrets)
	if len(bad) > 0 {
		return fmt.Errorf("invalid --secret value(s) %q: use KEY=VALUE, key must match [A-Za-z_][A-Za-z0-9_]*", bad)
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

	app, err := dep.CreateUpload(ctx, deployer.UploadRequest{
		Name:           strings.ToLower(strings.TrimSpace(uploadName)),
		Kind:           kind,
		Runtime:        fb.Language,
		RuntimeVersion: fb.Version,
		Entrypoint:     fb.Entrypoint,
		SourceRef:      filepath.Base(archive),
		Env:            env,
		Secrets:        secrets,
		ArchivePath:    archive,
		ScaleToZero:    uploadS2Z,
		IdleTimeout:    uploadIdle,
	})
	if err != nil {
		return err
	}
	au.Record(ctx, audit.Event{
		Action:     audit.ActionAppUpload,
		TargetType: "app",
		TargetID:   app.ID,
		TargetName: app.Name,
		Outcome:    audit.OutcomeSuccess,
		Detail:     "archive " + app.SourceRef,
	})
	if err := dep.Redeploy(ctx, app); err != nil {
		return err
	}
	if cfg.PublicHost != "" && cfg.AppPathPrefix != "" {
		fmt.Printf("Traefik config written; reachable at: %s\n", publicAppURL(cfg.PublicHost, cfg.AppPathPrefix, app.Name))
	}
	fmt.Printf("OK: app %s is running.\n", app.Name)
	return nil
}
