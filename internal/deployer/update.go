package deployer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/uuid"

	"github.com/albertoruiz/space-elevator/internal/audit"
	"github.com/albertoruiz/space-elevator/internal/builder"
	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/store"
)

// UpdateRequest replaces an app's source while retaining its stable app ID,
// slug, domains, environment, secrets, and managed persistent storage.
type UpdateRequest struct {
	Name        string
	Ref         string
	ArchivePath string
	SourceRef   string
	Secrets     map[string]string
}

// recovery records what a failed update managed to put back, so the
// failure path can report an honest terminal state instead of guessing
// from the error text. A nil *recovery means nothing was restored.
type recovery struct {
	// operationStatus is the terminal operation status (rolled_back).
	operationStatus string
	// appStatus is the app row status to record. It mirrors the pre-update
	// state — "running" or "stopped" — and is never forced to running.
	appStatus string
	// suffix annotates the user-visible error.
	suffix string
}

// UpdateResult describes a completed update. BackupDir is empty only when
// there was no persistent storage to snapshot.
type UpdateResult struct {
	App        *store.App
	Release    *store.AppRelease
	BackupDir  string
	RolledBack bool
}

// Update runs the safe brief-downtime update transaction. Source acquisition,
// validation, image build, and volume creation happen before the old app is
// stopped. Storage is backed up after the stop, then the candidate is
// activated. A failed activation attempts to recreate the previous release;
// the backup is retained for explicit recovery if the old release cannot start.
func (d *Deployer) Update(ctx context.Context, req UpdateRequest) (*UpdateResult, error) {
	return d.update(ctx, req, nil)
}

// UpdateClaimed is the web/API form of Update. The operation is claimed
// synchronously by the caller so a second click receives a conflict before
// the HTTP handler returns.
func (d *Deployer) UpdateClaimed(ctx context.Context, req UpdateRequest, operation *store.AppOperation) (*UpdateResult, error) {
	return d.update(ctx, req, operation)
}

func (d *Deployer) update(ctx context.Context, req UpdateRequest, claimed *store.AppOperation) (*UpdateResult, error) {
	name := strings.TrimSpace(req.Name)
	app, err := d.Store.GetAppByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if req.ArchivePath != "" && app.SourceType != "drop" {
		return nil, fmt.Errorf("app %q is a Git app; --archive is only valid for archive apps", name)
	}
	if req.ArchivePath == "" && app.SourceType != "git" {
		return nil, fmt.Errorf("app %q is an archive app; provide --archive to update it", name)
	}
	if req.ArchivePath != "" && strings.TrimSpace(req.Ref) != "" {
		return nil, fmt.Errorf("--ref and --archive cannot be used together")
	}
	operation := claimed
	if operation == nil {
		operation, err = d.Store.ClaimAppOperation(ctx, app.ID, "update")
		if err != nil {
			return nil, err
		}
	} else if operation.AppID != app.ID {
		return nil, fmt.Errorf("update operation belongs to another app")
	}
	finished := false
	finish := func(status, opErr string) {
		if finished {
			return
		}
		if err := d.Store.FinishOperation(context.Background(), operation.ID, status, opErr); err != nil {
			d.Opts.Log("warning: finalize operation %s: %v", operation.ID, err)
		}
		finished = true
	}
	defer func() {
		if !finished {
			finish(store.OperationStatusFailed, "update interrupted")
		}
	}()

	var restoreSecrets func() error
	secretsRestored := false
	restoreSecretsForRollback := func() error {
		if restoreSecrets == nil || secretsRestored {
			return nil
		}
		if err := restoreSecrets(); err != nil {
			return err
		}
		secretsRestored = true
		return nil
	}

	// recovered is set only by the paths that actually restore the pre-update
	// runtime; updateFailure turns it into an honest terminal state instead of
	// guessing from the error text.
	var recovered *recovery
	failUpdate := func(release *store.AppRelease, backupDir string, updateErr error) (*UpdateResult, error) {
		for _, secret := range req.Secrets {
			updateErr = redactErrorValue(updateErr, secret)
		}
		if rollbackErr := restoreSecretsForRollback(); rollbackErr != nil {
			updateErr = fmt.Errorf("%w (secret rollback failed: %v)", updateErr, rollbackErr)
			// A failed secret restore means the previous release is NOT
			// trustworthy even if the containers came back up.
			recovered = nil
		}
		return d.updateFailure(ctx, app, operation, release, backupDir, updateErr, recovered, finish)
	}

	if err := d.Store.UpdateAppStatusErr(ctx, app.ID, "updating", ""); err != nil {
		finish(store.OperationStatusFailed, err.Error())
		return nil, err
	}
	releaseID := uuid.NewString()
	releaseDir := releaseDir(d.Opts.AppsRoot, app.ID, releaseID)
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0o755); err != nil {
		return failUpdate(nil, "", err)
	}
	if err := os.RemoveAll(releaseDir); err != nil {
		return failUpdate(nil, "", fmt.Errorf("clear release directory: %w", err))
	}
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return failUpdate(nil, "", fmt.Errorf("create release directory: %w", err))
	}

	d.Opts.Stage("preflight")
	d.Opts.Log("Preparing %s update...", app.Name)
	candidate := *app
	candidate.Status = "updating"
	candidate.CurrentReleaseID = releaseID
	var gitCommit string
	if app.SourceType == "git" {
		ref := strings.TrimSpace(req.Ref)
		if ref == "" {
			ref = app.GitRef
		}
		if ref == "" {
			ref = "main"
		}
		sourceURL := builder.NormalizeGitURL(app.SourceRef)
		if err := builder.Clone(ctx, sourceURL, ref, releaseDir, d.gitAuth(ctx, sourceURL)); err != nil {
			return failUpdate(nil, "", fmt.Errorf("git clone: %w", err))
		}
		candidate.SourceRef = sourceURL
		repo, err := git.PlainOpen(releaseDir)
		if err != nil {
			return failUpdate(nil, "", fmt.Errorf("open cloned repository: %w", err))
		}
		head, err := repo.Head()
		if err != nil {
			return failUpdate(nil, "", fmt.Errorf("resolve cloned commit: %w", err))
		}
		gitCommit = head.Hash().String()
		candidate.GitRef = ref
		composeBytes, err := d.resolveCompose(ctx, &candidate, releaseDir)
		if err != nil {
			return failUpdate(nil, "", err)
		}
		candidate.ComposeYAML = string(composeBytes)
	} else {
		if err := builder.ExtractArchive(req.ArchivePath, releaseDir); err != nil {
			return failUpdate(nil, "", fmt.Errorf("extract archive: %w", err))
		}
		if candidate.Kind == store.KindFunction {
			fb := builder.FunctionBuild{Language: candidate.Runtime, Version: candidate.RuntimeVersion, Entrypoint: candidate.Entrypoint, EnvKeys: envKeys(candidate.Env)}
			if err := builder.WriteFunctionBuild(releaseDir, fb); err != nil {
				return failUpdate(nil, "", fmt.Errorf("function build: %w", err))
			}
			candidate.ComposeYAML = fb.Compose()
		} else {
			if candidate.Kind == store.KindCustom && !builder.HasDockerfile(releaseDir) {
				return failUpdate(nil, "", fmt.Errorf("custom update requires a Dockerfile in the archive"))
			}
			detected, err := builder.DetectKind(releaseDir)
			if err != nil {
				return failUpdate(nil, "", err)
			}
			candidate.DropKind = detected
			candidate.ComposeYAML = builder.SyntheticCompose()
			if detected == builder.DropKindStatic {
				baseHref := "/"
				if len(domainsForSync(ctx, d, app)) == 0 && d.Opts.AppPathPrefix != "" {
					baseHref = strings.TrimRight(d.Opts.AppPathPrefix, "/") + "/" + app.Slug + "/"
				}
				if _, err := builder.WriteStaticFiles(releaseDir, baseHref); err != nil {
					return failUpdate(nil, "", fmt.Errorf("static files: %w", err))
				}
			}
		}
		candidate.SourceRef = safeSourceRef(req.SourceRef, filepath.Base(req.ArchivePath))
	}
	spec, err := composer.Parse([]byte(candidate.ComposeYAML))
	if err != nil {
		return failUpdate(nil, "", fmt.Errorf("candidate compose: %w", err))
	}

	oldSpec := parseStoredSpec(app.ComposeYAML)
	oldSourceDir := ""
	if current, currentErr := d.Store.GetCurrentRelease(ctx, app.ID); currentErr == nil && current.SourcePath != "" {
		oldSourceDir, err = d.safeReleaseSource(app.ID, current.SourcePath)
		if err != nil {
			return failUpdate(nil, "", fmt.Errorf("previous release source: %w", err))
		}
		oldSpec = parseStoredSpec(current.ComposeYAML)
	}
	if oldSourceDir == "" {
		oldSourceDir, _ = store.AppSourceDir(d.Opts.AppsRoot, app)
	}
	existingStorage, err := d.Store.ListAppStorage(ctx, app.ID)
	if err != nil {
		return failUpdate(nil, "", err)
	}
	oldStorage := make([]*store.AppStorage, 0, len(existingStorage))
	for _, mapping := range existingStorage {
		if mapping != nil {
			copy := *mapping
			oldStorage = append(oldStorage, &copy)
		}
	}
	restoreStorageState := func() error {
		current, err := d.Store.ListAppStorage(context.Background(), app.ID)
		if err != nil {
			return err
		}
		for _, mapping := range current {
			if mapping == nil {
				continue
			}
			if err := d.Store.DeleteAppStorage(context.Background(), app.ID, mapping.LogicalName); err != nil {
				return err
			}
		}
		for _, mapping := range oldStorage {
			if err := d.Store.UpsertAppStorage(context.Background(), mapping); err != nil {
				return err
			}
		}
		return nil
	}
	plan, err := d.planStorage(ctx, app, oldSpec, oldSourceDir, spec, existingStorage)
	if err != nil {
		return failUpdate(nil, "", fmt.Errorf("storage preflight: %w", err))
	}
	for _, warning := range plan.Warnings {
		d.Opts.Log("warning: %s", warning)
	}

	release := &store.AppRelease{
		ID: releaseID, AppID: app.ID, PreviousReleaseID: app.CurrentReleaseID,
		SourceType: candidate.SourceType, SourceRef: candidate.SourceRef, GitRef: candidate.GitRef,
		GitCommit: gitCommit, SourcePath: releaseDir, ComposeYAML: candidate.ComposeYAML,
		Kind: candidate.Kind, BuildMode: candidate.BuildMode, BuilderImage: candidate.BuilderImage,
		BuildCommand: candidate.BuildCommand, RunCommand: candidate.RunCommand, ServePath: candidate.ServePath, ListenPort: candidate.ListenPort,
		Status: "preparing", ImageMap: map[string]string{},
	}
	if err := d.Store.CreateRelease(ctx, release); err != nil {
		return failUpdate(nil, "", err)
	}
	if len(req.Secrets) > 0 {
		previousSecrets, err := d.Store.GetSecrets(ctx, app.ID)
		if err != nil {
			return failUpdate(release, "", err)
		}
		snapshot := make(map[string]string, len(previousSecrets))
		for k, v := range previousSecrets {
			snapshot[k] = v
		}
		restoreSecrets = func() error {
			return d.replaceSecrets(ctx, app.ID, snapshot)
		}
	}
	for k, v := range req.Secrets {
		if err := d.Store.SetSecret(ctx, app.ID, k, v); err != nil {
			return failUpdate(release, "", fmt.Errorf("store secret %q: %w", k, err))
		}
	}

	if err := d.Store.SetOperationStatus(ctx, operation.ID, store.OperationStatusBuilding); err != nil {
		return failUpdate(release, "", err)
	}
	d.Opts.Stage("build")
	meta, err := updateRuntimeMeta(ctx, app, &candidate, releaseID, plan.Candidate, d.Runtime, d.Store)
	if err != nil {
		return failUpdate(release, "", fmt.Errorf("load candidate environment: %w", err))
	}
	images, err := d.Runtime.Prepare(ctx, meta, spec, releaseDir)
	if err != nil {
		return failUpdate(release, "", fmt.Errorf("prepare release: %w", err))
	}
	release.ImageMap = images
	if err := d.Store.UpdateRelease(ctx, release); err != nil {
		return failUpdate(release, "", err)
	}

	if err := d.Store.SetOperationStatus(ctx, operation.ID, store.OperationStatusBackingUp); err != nil {
		return failUpdate(release, "", err)
	}
	d.Opts.Stage("backup")
	// A backup is only meaningful after the old workload is actually stopped.
	// A missing container set is safe to skip when the app is already stopped;
	// any other stop failure aborts before archiving live storage.
	statusSpec := oldSpec
	if statusSpec == nil {
		statusSpec = &composer.Spec{}
	}
	statusBeforeBackup, _, statusErr := d.Runtime.Status(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}, statusSpec)
	if statusErr != nil {
		return failUpdate(release, "", fmt.Errorf("inspect previous runtime: %w", statusErr))
	}
	// weStoppedApp is true when this update is the thing that stopped the
	// app, i.e. it was running before we began. Recovery may only restart
	// what we took down: an app the operator had already stopped must stay
	// stopped across a failed update.
	//
	// Note the polarity — this is the opposite of "was stopped", and
	// getting it backwards silently flips a stopped app to running (or
	// takes a healthy app down) after a failed update.
	weStoppedApp := statusBeforeBackup != "stopped"
	if weStoppedApp {
		if err := d.Runtime.Stop(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}); err != nil {
			return failUpdate(release, "", fmt.Errorf("stop previous runtime: %w", err))
		}
	}

	// resumePrevious restores the pre-update runtime state (secrets plus,
	// only if we stopped it, the containers) and folds both failures into
	// the reported cause. The recovered state it records mirrors what the
	// app looked like before the update, not merely what this run touched.
	resumePrevious := func(cause string, causeErr error, backup string) (*UpdateResult, error) {
		secretErr := restoreSecretsForRollback()
		var restartErr error
		if weStoppedApp {
			restartErr = d.restartPrevious(ctx, app)
		}
		if secretErr != nil {
			return failUpdate(release, backup, fmt.Errorf("%s: %v; secret rollback failed: %w", cause, causeErr, secretErr))
		}
		if restartErr != nil {
			return failUpdate(release, backup, fmt.Errorf("%s: %v; restart previous: %w", cause, causeErr, restartErr))
		}
		if weStoppedApp {
			// We stopped it, so putting it back to running restores the
			// pre-update state.
			recovered = &recovery{
				operationStatus: store.OperationStatusRolledBack,
				appStatus:       "running",
				suffix:          " (previous release restored)",
			}
		} else {
			// Found stopped, left stopped: the update is a no-op on the
			// running workload, so the app must not be flipped to running.
			recovered = &recovery{
				operationStatus: store.OperationStatusRolledBack,
				appStatus:       "stopped",
				suffix:          " (app left stopped as found)",
			}
		}
		return failUpdate(release, backup, fmt.Errorf("%s: %w%s", cause, causeErr, recovered.suffix))
	}

	backupDir, err := d.backupStorage(ctx, app.ID, releaseID, plan.All)
	if err != nil {
		return resumePrevious("backup storage", err, "")
	}
	for oldPath, newPath := range plan.MigrateBinds {
		if err := copyDirectory(oldPath, newPath); err != nil && !os.IsNotExist(err) {
			return resumePrevious(fmt.Sprintf("migrate bind %q", oldPath), err, backupDir)
		}
	}

	restoreAppState := func() error {
		restored := *app
		if err := d.Store.UpdateApp(context.Background(), &restored); err != nil {
			return err
		}
		if app.CurrentReleaseID == "" {
			return d.Store.ClearCurrentRelease(context.Background(), app.ID)
		}
		return d.Store.SetCurrentRelease(context.Background(), app.ID, app.CurrentReleaseID)
	}
	rollbackCandidate := func(reason error) (*UpdateResult, error) {
		d.Opts.Stage("rollback")
		secretErr := restoreSecretsForRollback()
		rollbackErr := d.rollbackUpdate(ctx, app, oldSpec, oldSourceDir, plan)
		stateErr := restoreStorageState()
		appErr := restoreAppState()
		if appErr == nil {
			if routeErr := d.SyncRoute(context.Background(), app); routeErr != nil {
				appErr = fmt.Errorf("restore route: %w", routeErr)
			}
		}
		if secretErr != nil {
			return failUpdate(release, backupDir, fmt.Errorf("%v; secret rollback failed: %w", reason, secretErr))
		}
		if rollbackErr != nil {
			return failUpdate(release, backupDir, fmt.Errorf("%v; rollback failed: %w", reason, rollbackErr))
		}
		if stateErr != nil || appErr != nil {
			return failUpdate(release, backupDir, fmt.Errorf("%v; state restore failed: %v", reason, firstError(stateErr, appErr)))
		}
		// rollbackUpdate recreates and *starts* the previous release, so it
		// needs a correction only when this update did not stop the app.
		if weStoppedApp {
			// The app was running before this update, so a running previous
			// release is exactly the pre-update state.
			recovered = &recovery{
				operationStatus: store.OperationStatusRolledBack,
				appStatus:       "running",
				suffix:          " (previous release restored)",
			}
			return failUpdate(release, backupDir, fmt.Errorf("%w%s", reason, recovered.suffix))
		}
		// The operator had this app stopped; rollback just started it. Put it
		// back so a failed update never resurrects a deliberately stopped app.
		if err := d.Runtime.Stop(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}); err != nil {
			return failUpdate(release, backupDir, fmt.Errorf("%v; re-stop previous release: %w", reason, err))
		}
		recovered = &recovery{
			operationStatus: store.OperationStatusRolledBack,
			appStatus:       "stopped",
			suffix:          " (app left stopped as found)",
		}
		return failUpdate(release, backupDir, fmt.Errorf("%w%s", reason, recovered.suffix))
	}

	if err := d.Store.SetOperationStatus(ctx, operation.ID, store.OperationStatusCuttingOver); err != nil {
		return failUpdate(release, backupDir, err)
	}
	d.Opts.Stage("deploy")
	if err := d.Runtime.RemoveContainers(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}); err != nil {
		return rollbackCandidate(fmt.Errorf("remove previous containers: %w", err))
	}
	if err := d.Runtime.Activate(ctx, meta, spec, releaseDir, images); err != nil {
		return rollbackCandidate(fmt.Errorf("activate release: %w", err))
	}

	if err := d.Store.SetOperationStatus(ctx, operation.ID, store.OperationStatusVerifying); err != nil {
		return rollbackCandidate(err)
	}
	// Runtime.Activate has no generic readiness contract. A short stable
	// window catches immediate crash loops; Compose health checks can be added
	// later without changing the update transaction.
	if err := verifyRuntime(ctx, d, app, spec); err != nil {
		return rollbackCandidate(fmt.Errorf("verify release: %w", err))
	}

	d.Opts.Stage("route")
	if err := d.applyRoute(ctx, &candidate, spec); err != nil {
		return rollbackCandidate(fmt.Errorf("publish route: %w", err))
	}
	release.Status = "running"
	release.CompletedAt = time.Now()
	if err := d.Store.UpdateRelease(ctx, release); err != nil {
		return rollbackCandidate(err)
	}
	candidate.Status = "running"
	candidate.LastError = ""
	candidate.CurrentReleaseID = releaseID
	if err := d.Store.UpdateApp(ctx, &candidate); err != nil {
		return rollbackCandidate(err)
	}
	if err := d.Store.SetCurrentRelease(ctx, app.ID, releaseID); err != nil {
		return rollbackCandidate(err)
	}
	if err := d.persistRuntimeStorage(ctx, app, plan); err != nil {
		return rollbackCandidate(err)
	}
	restoreSecrets = nil
	d.audit(ctx, audit.ActionAppUpdate, &candidate, audit.OutcomeSuccess, "update release "+releaseID+" backup "+backupDir)
	finish(store.OperationStatusCompleted, "")
	return &UpdateResult{App: &candidate, Release: release, BackupDir: backupDir}, nil
}

func (d *Deployer) replaceSecrets(ctx context.Context, appID string, desired map[string]string) error {
	current, err := d.Store.GetSecrets(ctx, appID)
	if err != nil {
		return err
	}
	for key := range current {
		if _, keep := desired[key]; !keep {
			if err := d.Store.DeleteSecret(ctx, appID, key); err != nil {
				return err
			}
		}
	}
	for key, value := range desired {
		if err := d.Store.SetSecret(ctx, appID, key, value); err != nil {
			return err
		}
	}
	return nil
}

func parseStoredSpec(value string) *composer.Spec {
	spec, err := composer.Parse([]byte(value))
	if err != nil {
		return nil
	}
	return spec
}

func updateRuntimeMeta(ctx context.Context, app, candidate *store.App, releaseID string, storage map[string]composer.StorageBinding, runtime *composer.Runtime, st *store.Store) (composer.AppMeta, error) {
	spec := parseStoredSpec(candidate.ComposeYAML)
	if spec == nil {
		return composer.AppMeta{}, fmt.Errorf("candidate compose is invalid")
	}
	meta := releaseMeta(candidate, releaseID, spec, storage, runtime)
	runtimeEnv, err := st.LoadRuntimeEnv(ctx, app.ID, candidate.Env)
	if err != nil {
		return composer.AppMeta{}, err
	}
	meta.Env = runtimeEnv
	meta.BuildEnv = candidate.Env
	meta.StaticDrop = candidate.SourceType == "drop" && candidate.DropKind == builder.DropKindStatic
	return meta, nil
}

func domainsForSync(ctx context.Context, d *Deployer, app *store.App) []string {
	domains, _ := d.Store.GetAppDomains(ctx, app.ID)
	return domains
}

func verifyRuntime(ctx context.Context, d *Deployer, app *store.App, spec *composer.Spec) error {
	// Do not wait for a health check that the custom compose subset cannot yet
	// express. Runtime.Status still catches a container that failed to start.
	status, _, err := d.Runtime.Status(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}, spec)
	if err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("new release is %s", status)
	}
	return nil
}

func (d *Deployer) restartPrevious(ctx context.Context, app *store.App) error {
	return d.Runtime.Start(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug})
}

func (d *Deployer) rollbackUpdate(ctx context.Context, app *store.App, oldSpec *composer.Spec, oldSourceDir string, plan *storagePlan) error {
	if oldSpec == nil || oldSourceDir == "" {
		return fmt.Errorf("previous release source is unavailable")
	}
	if err := d.Runtime.RemoveContainers(ctx, composer.AppMeta{ID: app.ID, Name: app.Slug, Label: app.Slug}); err != nil {
		return fmt.Errorf("remove candidate containers: %w", err)
	}
	oldMeta := releaseMeta(app, "previous", oldSpec, plan.Previous, d.Runtime)
	oldEnv, err := d.Store.LoadRuntimeEnv(ctx, app.ID, app.Env)
	if err != nil {
		return err
	}
	oldMeta.Env = oldEnv
	oldMeta.BuildEnv = app.Env
	oldMeta.StaticDrop = app.SourceType == "drop" && app.DropKind == builder.DropKindStatic
	if err := d.Runtime.Deploy(ctx, oldMeta, oldSpec, oldSourceDir); err != nil {
		return err
	}
	status, _, err := d.Runtime.Status(ctx, oldMeta, oldSpec)
	if err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("previous release is %s after rollback", status)
	}
	return nil
}

func redactErrorValue(err error, value string) error {
	if err == nil || value == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), value, "[redacted]")
	return fmt.Errorf("%s", message)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// updateFailure records the terminal state of a failed update. The caller
// passes the recovery it achieved (nil when nothing was restored); the
// terminal operation and app status follow from that rather than from
// pattern-matching the error text.
func (d *Deployer) updateFailure(ctx context.Context, app *store.App, operation *store.AppOperation, release *store.AppRelease, backupDir string, updateErr error, recovered *recovery, finish func(string, string)) (*UpdateResult, error) {
	if release != nil {
		release.Status = "failed"
		release.LastError = updateErr.Error()
		release.CompletedAt = time.Now()
		_ = d.Store.UpdateRelease(context.Background(), release)
	}
	operationStatus := store.OperationStatusFailed
	appStatus := "error"
	appError := updateErr.Error()
	switch {
	case recovered != nil:
		operationStatus = recovered.operationStatus
		appStatus = recovered.appStatus
		appError = ""
	case strings.Contains(updateErr.Error(), "rollback failed"):
		operationStatus = store.OperationStatusRollbackFailed
	}
	_ = d.Store.UpdateAppStatusErr(context.Background(), app.ID, appStatus, appError)
	finish(operationStatus, updateErr.Error())
	d.audit(ctx, audit.ActionAppUpdate, app, audit.OutcomeFailure, updateErr.Error()+" backup "+backupDir)
	return nil, updateErr
}
