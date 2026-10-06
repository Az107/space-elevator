package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/selfupdate"
	"github.com/albertoruiz/space-elevator/internal/service"
	"github.com/albertoruiz/space-elevator/internal/store"
	"github.com/albertoruiz/space-elevator/internal/version"
)

var (
	updateCheck    bool
	updateForce    bool
	updateRollback bool
	updateRepoFlag string
	updateTarget   string
)

var errNoUpdateRepo = errors.New(
	"update repository is not configured; set update_repo (SPACE_ELEVATOR_UPDATE_REPO) or pass --repo")

// UpdateCmd updates the space-elevator binary from a published release.
// It is deliberately CLI-only: the dashboard runs as the service being
// restarted, so it cannot safely replace and restart itself.
var UpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the space-elevator binary from a published release",
	Long: "Download, verify and install a newer published release of\n" +
		"space-elevator, then restart the systemd user service and health-check\n" +
		"it. The previous binary is kept as <binary>.prev and restored\n" +
		"automatically if the new one does not come up. Run it as the user that\n" +
		"owns the installed service (production runs as `services`).\n\n" +
		"This updates the space-elevator binary itself; to update a deployed\n" +
		"app use `space-elevator apps update <app>`.",
	Args: cobra.NoArgs,
	RunE: runUpdate,
}

func init() {
	UpdateCmd.Flags().BoolVar(&updateCheck, "check", false, "report the latest release without changing anything")
	UpdateCmd.Flags().BoolVar(&updateForce, "force", false, "install even when the current version is not comparable or not older")
	UpdateCmd.Flags().BoolVar(&updateRollback, "rollback", false, "restore the previous binary (<binary>.prev) and restart")
	UpdateCmd.Flags().StringVar(&updateRepoFlag, "repo", "", "GitHub repository to update from (default: update_repo)")
	UpdateCmd.Flags().StringVar(&updateTarget, "target", "", "binary to replace (default: the installed unit's ExecStart)")
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()
	ctx := cmd.Context()

	repo := updateRepoFlag
	if repo == "" {
		repo = cfg.UpdateRepo
	}

	target, mismatch, err := resolveUpdateTarget()
	if err != nil {
		return err
	}

	if updateRollback {
		if mismatch != "" {
			return errors.New(mismatch)
		}
		return runUpdateRollback(ctx, cmd, cfg, target)
	}
	if updateCheck {
		// --check is read-only and useful for diagnosis even from the wrong
		// binary, so a target mismatch is only a warning here.
		if mismatch != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "warn: "+mismatch)
		}
		return runUpdateCheck(ctx, cmd, cfg, repo, target)
	}
	if mismatch != "" {
		return errors.New(mismatch)
	}
	return runUpdateApply(ctx, cmd, cfg, repo, target)
}

// resolveUpdateTarget decides which binary to replace. The installed unit is
// authoritative: it names the binary the restart will execute. The running
// executable must be that binary, otherwise the update would replace one file
// and restart another — a silent no-op. mismatch is non-empty when the two
// disagree; callers decide whether that is fatal.
func resolveUpdateTarget() (target, mismatch string, err error) {
	if updateTarget != "" {
		abs, err := filepath.Abs(updateTarget)
		if err != nil {
			return "", "", err
		}
		return abs, "", nil
	}
	self, err := service.SelfBinary()
	if err != nil {
		return "", "", fmt.Errorf("resolve running executable: %w", err)
	}
	unit := service.UnitPath()
	if _, statErr := os.Stat(unit); statErr == nil {
		if fromUnit, err := selfupdate.TargetFromUnit(unit); err == nil && fromUnit != "" {
			if samePath(fromUnit, self) {
				return self, "", nil
			}
			return fromUnit, fmt.Sprintf(
				"this executable is %s but the installed unit runs %s\n"+
					"run the update from the installed binary, or pass --target explicitly", self, fromUnit), nil
		}
	}
	return self, "", nil
}

func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func runUpdateCheck(ctx context.Context, cmd *cobra.Command, cfg *config.Config, repo, target string) error {
	if repo == "" {
		return errNoUpdateRepo
	}
	st, closeStore, _ := openUpdateStore(cfg)
	defer closeStore()

	client, err := newUpdateClient(ctx, st, repo)
	if err != nil {
		return err
	}
	rel, asset, sha, err := latestFor(ctx, client)
	if err != nil {
		return describeReleaseErr(err)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "current: %s\n", version.String())
	fmt.Fprintf(w, "latest:  %s\n", rel.TagName)
	fmt.Fprintf(w, "asset:   %s (%d bytes)\n", asset.Name, asset.Size)

	if local, err := selfupdate.FileSHA(target); err == nil && local == sha {
		fmt.Fprintf(w, "status:  already up to date (%s matches the installed binary)\n", target)
		return nil
	}
	if version.IsRelease() {
		if cmp, ok := version.Compare(rel.TagName, version.Version); ok {
			if cmp > 0 {
				fmt.Fprintln(w, "status:  update available")
			} else {
				fmt.Fprintln(w, "status:  installed version is not older than the latest release")
			}
			return nil
		}
	}
	fmt.Fprintln(w, "status:  current build is not a comparable release version (use --force to install)")
	return nil
}

func runUpdateApply(ctx context.Context, cmd *cobra.Command, cfg *config.Config, repo, target string) error {
	if repo == "" {
		return errNoUpdateRepo
	}

	st, closeStore, err := openUpdateStore(cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	ctx, au := cliAudit(ctx, st)

	if !updateForce {
		busy, err := st.HasAnyActiveOperation(ctx)
		if err != nil {
			return err
		}
		if busy {
			return errors.New("a deploy or update is in progress; wait for it to finish before updating space-elevator")
		}
	}

	release, err := acquireUpdateLock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer release()

	client, err := newUpdateClient(ctx, st, repo)
	if err != nil {
		return err
	}
	rel, asset, sha, err := latestFor(ctx, client)
	if err != nil {
		return describeReleaseErr(err)
	}

	inst := &selfupdate.Installer{Target: target, Log: func(f string, a ...any) { fmt.Fprintf(cmd.OutOrStdout(), f+"\n", a...) }}

	if local, err := selfupdate.FileSHA(target); err == nil && local == sha {
		fmt.Fprintf(cmd.OutOrStdout(), "Already up to date: %s is %s.\n", target, rel.TagName)
		return nil
	}
	if !updateForce {
		if !version.IsRelease() {
			return fmt.Errorf("current build is not a release version (%q); re-run with --force to install %s", version.Version, rel.TagName)
		}
		cmp, ok := version.Compare(rel.TagName, version.Version)
		if !ok {
			return fmt.Errorf("cannot compare %q with %q; re-run with --force", version.Version, rel.TagName)
		}
		if cmp <= 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Already up to date: %s (latest %s).\n", version.Version, rel.TagName)
			return nil
		}
	}
	if err := inst.Preflight(); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Updating %s -> %s (asset %s)...\n", version.Version, rel.TagName, asset.Name)

	tmp, got, err := stageAsset(ctx, client, inst, asset)
	if err != nil {
		return err
	}
	if got != sha {
		_ = os.Remove(tmp)
		au.Record(ctx, audit.Event{Action: audit.ActionSelfUpdate, TargetType: "system", TargetName: rel.TagName, Outcome: audit.OutcomeFailure, Detail: "checksum mismatch"})
		return fmt.Errorf("checksum mismatch: downloaded %s, release records %s", got, sha)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "checksum: ok (%s)\n", short(got))
	if err := selfupdate.SmokeTest(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := inst.Commit(tmp, sha); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := restartAndVerify(ctx, cfg); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "update failed health check: %v\nrolling back to the previous binary...\n", err)
		if rerr := inst.Restore(); rerr != nil {
			return fmt.Errorf("update failed (%v) and rollback failed: %w", err, rerr)
		}
		if rerr := restartAndVerify(ctx, cfg); rerr != nil {
			return fmt.Errorf("rolled back but the previous binary is unhealthy too: %w", rerr)
		}
		au.Record(ctx, audit.Event{Action: audit.ActionSelfUpdate, TargetType: "system", TargetName: rel.TagName, Outcome: audit.OutcomeFailure, Detail: "rolled back after failed health check"})
		return fmt.Errorf("update rolled back: %v", err)
	}

	au.Record(ctx, audit.Event{Action: audit.ActionSelfUpdate, TargetType: "system", TargetName: rel.TagName, Outcome: audit.OutcomeSuccess})
	fmt.Fprintf(cmd.OutOrStdout(), "OK: updated %s -> %s and restarted.\n", target, rel.TagName)
	return nil
}

func runUpdateRollback(ctx context.Context, cmd *cobra.Command, cfg *config.Config, target string) error {
	inst := &selfupdate.Installer{Target: target, Log: func(f string, a ...any) { fmt.Fprintf(cmd.OutOrStdout(), f+"\n", a...) }}
	if !inst.HasPrevious() {
		return fmt.Errorf("no previous binary at %s.prev; nothing to roll back to", target)
	}
	if err := inst.Restore(); err != nil {
		return err
	}
	if err := restartAndVerify(ctx, cfg); err != nil {
		return fmt.Errorf("restored the previous binary but it is not healthy: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OK: restored the previous binary at %s and restarted.\n", target)
	return nil
}

// stageAsset streams the asset through Stage so it is hashed while written.
func stageAsset(ctx context.Context, client *selfupdate.Client, inst *selfupdate.Installer, asset *selfupdate.Asset) (string, string, error) {
	pr, pw := io.Pipe()
	go func() {
		_, derr := client.DownloadAsset(ctx, asset, pw)
		_ = pw.CloseWithError(derr)
	}()
	tmp, sha, err := inst.Stage(pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return "", "", fmt.Errorf("download %s: %w", asset.Name, err)
	}
	return tmp, sha, nil
}

// restartAndVerify restarts the unit and waits for the service to be active
// and the dashboard root to answer below 400.
func restartAndVerify(ctx context.Context, cfg *config.Config) error {
	if err := service.Available(); err != nil {
		return fmt.Errorf("cannot restart the service: %w", err)
	}
	if _, err := service.Control("restart"); err != nil {
		return err
	}
	url := selfupdate.ProbeURL(cfg.BindAddr)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if service.Query().Active && selfupdate.Healthy(ctx, url) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service not healthy at %s within 30s", url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// openUpdateStore opens the state DB for the audit log, the token lookup and
// the active-operation guard. A missing DB is not fatal for `--check`; the
// apply path decides for itself.
func openUpdateStore(cfg *config.Config) (*store.Store, func(), error) {
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return nil, func() {}, err
	}
	return st, func() { _ = st.Close() }, nil
}

func newUpdateClient(ctx context.Context, st *store.Store, repo string) (*selfupdate.Client, error) {
	token := ""
	if st != nil {
		if _, _, host, err := selfupdate.ParseRepo(repo); err == nil {
			if host == "" {
				host = "github.com"
			}
			if cred, err := st.GetCredentialForHost(ctx, host); err == nil {
				token = cred.Token
			}
		}
	}
	return selfupdate.NewClient(repo, token)
}

func latestFor(ctx context.Context, client *selfupdate.Client) (*selfupdate.Release, *selfupdate.Asset, string, error) {
	rel, err := client.Latest(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	asset, err := selfupdate.FindAsset(rel, selfupdate.AssetName(client.GOOS, client.GOARCH))
	if err != nil {
		return nil, nil, "", err
	}
	sha, err := client.ExpectedSHA(ctx, rel, asset.Name)
	if err != nil {
		return nil, nil, "", err
	}
	return rel, asset, sha, nil
}

func describeReleaseErr(err error) error {
	switch {
	case errors.Is(err, selfupdate.ErrNoRelease):
		return errors.New("no published release found (a draft or prerelease is invisible to update)")
	case errors.Is(err, selfupdate.ErrRateLimited):
		return errors.New("GitHub API rate limit exceeded; add a github.com git credential in the dashboard or retry later")
	}
	return err
}

func acquireUpdateLock(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "update.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another space-elevator update is already running")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
