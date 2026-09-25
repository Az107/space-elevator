package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrConflict is returned when a write cannot be completed because it
// conflicts with state already held by another writer. In particular,
// ClaimOperation uses it when an app already has an active operation.
var ErrConflict = errors.New("conflict")

const maxPersistedError = 4096

func boundedError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxPersistedError {
		return value[:maxPersistedError] + "…"
	}
	return value
}

const (
	StorageKindVolume = "volume"
	StorageKindBind   = "bind"
)

// AppStorage is the durable mapping between a logical storage name in an
// app's compose file and the physical Podman volume or host path used for it.
type AppStorage struct {
	AppID       string    `json:"app_id"`
	LogicalName string    `json:"logical_name"`
	StorageKind string    `json:"storage_kind"`
	PhysicalRef string    `json:"physical_ref"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AppStorageMapping is a descriptive alias for callers that use the
// mapping terminology from the schema.
type AppStorageMapping = AppStorage

const (
	OperationStatusPreflighting   = "preflighting"
	OperationStatusBuilding       = "building"
	OperationStatusBackingUp      = "backing_up"
	OperationStatusCuttingOver    = "cutting_over"
	OperationStatusVerifying      = "verifying"
	OperationStatusRollingBack    = "rolling_back"
	OperationStatusCompleted      = "completed"
	OperationStatusFailed         = "failed"
	OperationStatusRolledBack     = "rolled_back"
	OperationStatusRollbackFailed = "rollback_failed"
	OperationStatusCancelled      = "cancelled"
	OperationStatusInterrupted    = "interrupted"
)

// AppOperation records the update/deploy operation that owns an app. The
// operation status is deliberately persisted rather than inferred from a
// process so an interrupted update can be detected and recovered.
type AppOperation struct {
	ID                 string    `json:"id"`
	AppID              string    `json:"app_id"`
	OperationType      string    `json:"operation_type"`
	Status             string    `json:"status"`
	CurrentStep        string    `json:"current_step,omitempty"`
	Attempt            int       `json:"attempt"`
	PreviousReleaseID  string    `json:"previous_release_id,omitempty"`
	CandidateReleaseID string    `json:"candidate_release_id,omitempty"`
	HeartbeatAt        time.Time `json:"heartbeat_at,omitempty"`
	Error              string    `json:"error,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
}

// Operation is an alias for the shorter operation terminology used by
// operation-oriented callers.
type Operation = AppOperation

// AppRelease is the immutable source/build description for one deployed
// revision. A release can be updated as it progresses, but its identity and
// source snapshot remain separate from the app record.
type AppRelease struct {
	ID                string            `json:"id"`
	AppID             string            `json:"app_id"`
	PreviousReleaseID string            `json:"previous_release_id,omitempty"`
	SourceType        string            `json:"source_type"`
	SourceRef         string            `json:"source_ref"`
	GitRef            string            `json:"git_ref"`
	GitCommit         string            `json:"git_commit"`
	SourcePath        string            `json:"source_path"`
	ComposeYAML       string            `json:"compose_yaml"`
	Kind              string            `json:"kind"`
	BuildMode         string            `json:"build_mode"`
	BuilderImage      string            `json:"builder_image,omitempty"`
	BuildCommand      string            `json:"build_command,omitempty"`
	RunCommand        string            `json:"run_command,omitempty"`
	ServePath         string            `json:"serve_path,omitempty"`
	ListenPort        int               `json:"listen_port,omitempty"`
	ImageMap          map[string]string `json:"image_map,omitempty"`
	// ImageMapJSON is the exact persisted representation. It is accepted as
	// input for callers that already have JSON, and is populated on reads.
	ImageMapJSON string    `json:"image_map_json,omitempty"`
	Status       string    `json:"status"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
}

const appStorageColumns = `app_id, logical_name, storage_kind, physical_ref, created_at, updated_at`

const appReleaseColumns = `id, app_id, previous_release_id, source_type, source_ref,
	git_ref, git_commit, source_path, compose_yaml, kind, build_mode,
	builder_image, build_command, run_command, serve_path, listen_port, image_map_json,
	status, last_error, created_at, completed_at`

const appOperationColumns = `id, app_id, operation_type, status, current_step, attempt,
	previous_release_id, candidate_release_id, heartbeat_at, error, created_at,
	started_at, completed_at`

// UpsertAppStorage creates or replaces the mapping for an app's logical
// storage name. The created timestamp is retained when an existing mapping is
// updated.
func (s *Store) UpsertAppStorage(ctx context.Context, mapping *AppStorage) error {
	if mapping == nil {
		return errors.New("nil app storage mapping")
	}
	now := time.Now()
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = now
	}
	mapping.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_storage (app_id, logical_name, storage_kind, physical_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(app_id, logical_name) DO UPDATE SET
			storage_kind=excluded.storage_kind,
			physical_ref=excluded.physical_ref,
			updated_at=excluded.updated_at`,
		mapping.AppID, mapping.LogicalName, mapping.StorageKind, mapping.PhysicalRef,
		mapping.CreatedAt.Unix(), mapping.UpdatedAt.Unix())
	return err
}

func (s *Store) UpsertAppStorageMapping(ctx context.Context, mapping *AppStorageMapping) error {
	return s.UpsertAppStorage(ctx, mapping)
}

func (s *Store) UpsertStorageMapping(ctx context.Context, mapping *AppStorageMapping) error {
	return s.UpsertAppStorage(ctx, mapping)
}

// ListAppStorage returns mappings ordered by their logical names.
func (s *Store) ListAppStorage(ctx context.Context, appID string) ([]*AppStorage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appStorageColumns+`
		FROM app_storage WHERE app_id=? ORDER BY logical_name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AppStorage
	for rows.Next() {
		mapping, err := scanAppStorage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mapping)
	}
	return out, rows.Err()
}

func (s *Store) ListAppStorageMappings(ctx context.Context, appID string) ([]*AppStorageMapping, error) {
	return s.ListAppStorage(ctx, appID)
}

func (s *Store) ListStorageMappings(ctx context.Context, appID string) ([]*AppStorageMapping, error) {
	return s.ListAppStorage(ctx, appID)
}

// DeleteAppStorage removes one logical mapping. The app must still exist;
// removing an app removes all of its mappings through the foreign key.
func (s *Store) DeleteAppStorage(ctx context.Context, appID, logicalName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_storage WHERE app_id=? AND logical_name=?`, appID, logicalName)
	return err
}

func scanAppStorage(r rowScanner) (*AppStorage, error) {
	var mapping AppStorage
	var created, updated int64
	if err := r.Scan(&mapping.AppID, &mapping.LogicalName, &mapping.StorageKind,
		&mapping.PhysicalRef, &created, &updated); err != nil {
		return nil, err
	}
	mapping.CreatedAt = time.Unix(created, 0)
	mapping.UpdatedAt = time.Unix(updated, 0)
	return &mapping, nil
}

// releaseImageJSON returns a validated JSON representation, accepting either
// ImageMap or the raw ImageMapJSON field as input. ImageMap wins when both are
// populated so callers can mutate a map returned by a scan and update it.
func releaseImageJSON(r *AppRelease) (string, error) {
	var raw string
	if r.ImageMap != nil {
		b, err := json.Marshal(r.ImageMap)
		if err != nil {
			return "", fmt.Errorf("marshal release image map: %w", err)
		}
		raw = string(b)
	} else {
		raw = strings.TrimSpace(r.ImageMapJSON)
	}
	if raw == "" {
		raw = "{}"
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("invalid release image map JSON: %w", err)
	}
	if value == nil {
		return "{}", nil
	}
	return raw, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.Unix()
}

// CreateRelease persists a new release. Defaults match App's build defaults
// so releases created by older callers remain self-describing.
func (s *Store) CreateRelease(ctx context.Context, r *AppRelease) error {
	if r == nil {
		return errors.New("nil app release")
	}
	if r.BuildMode == "" {
		r.BuildMode = BuildModeCompose
	}
	if r.Kind == "" {
		r.Kind = KindWeb
	}
	imageJSON, err := releaseImageJSON(r)
	if err != nil {
		return err
	}
	r.ImageMapJSON = imageJSON
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_releases (`+appReleaseColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.AppID, nullableString(r.PreviousReleaseID), r.SourceType, r.SourceRef,
		r.GitRef, r.GitCommit, r.SourcePath, r.ComposeYAML, r.Kind, r.BuildMode,
		r.BuilderImage, r.BuildCommand, r.RunCommand, r.ServePath, r.ListenPort, r.ImageMapJSON,
		r.Status, r.LastError, r.CreatedAt.Unix(), nullableTime(r.CompletedAt))
	return err
}

func (s *Store) CreateAppRelease(ctx context.Context, r *AppRelease) error {
	return s.CreateRelease(ctx, r)
}

// GetRelease returns ErrNotFound for an unknown release.
func (s *Store) GetRelease(ctx context.Context, id string) (*AppRelease, error) {
	return s.queryRelease(ctx, `SELECT `+appReleaseColumns+` FROM app_releases WHERE id=?`, id)
}

func (s *Store) GetAppRelease(ctx context.Context, id string) (*AppRelease, error) {
	return s.GetRelease(ctx, id)
}

// ListReleases returns an app's releases newest-first.
func (s *Store) ListReleases(ctx context.Context, appID string) ([]*AppRelease, error) {
	return s.listReleases(ctx, `SELECT `+appReleaseColumns+`
		FROM app_releases WHERE app_id=? ORDER BY created_at DESC, rowid DESC`, appID)
}

func (s *Store) ListAppReleases(ctx context.Context, appID string) ([]*AppRelease, error) {
	return s.ListReleases(ctx, appID)
}

// GetCurrentRelease resolves apps.current_release_id. It returns
// ErrNotFound when the app has no current release (or the referenced release
// has been removed).
func (s *Store) GetCurrentRelease(ctx context.Context, appID string) (*AppRelease, error) {
	return s.queryRelease(ctx, `SELECT `+prefixReleaseColumns("r")+`
		FROM apps a JOIN app_releases r ON r.id=a.current_release_id
		WHERE a.id=?`, appID)
}

func (s *Store) GetAppCurrentRelease(ctx context.Context, appID string) (*AppRelease, error) {
	return s.GetCurrentRelease(ctx, appID)
}

func (s *Store) queryRelease(ctx context.Context, query string, args ...any) (*AppRelease, error) {
	row := contextRow(s.db, ctx, query, args...)
	r, err := scanAppRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) listReleases(ctx context.Context, query string, args ...any) ([]*AppRelease, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AppRelease
	for rows.Next() {
		r, err := scanAppRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// contextRow is kept separate to make the query helpers read like the other
// store methods while still using QueryRowContext consistently.
func contextRow(db *sql.DB, ctx context.Context, query string, args ...any) *sql.Row {
	return db.QueryRowContext(ctx, query, args...)
}

func prefixReleaseColumns(prefix string) string {
	columns := strings.Split(appReleaseColumns, ",")
	for i := range columns {
		columns[i] = prefix + "." + strings.TrimSpace(columns[i])
	}
	return strings.Join(columns, ", ")
}

func scanAppRelease(r rowScanner) (*AppRelease, error) {
	var release AppRelease
	var previousReleaseID sql.NullString
	var imageMapJSON string
	var created int64
	var completed sql.NullInt64
	if err := r.Scan(&release.ID, &release.AppID, &previousReleaseID, &release.SourceType,
		&release.SourceRef, &release.GitRef, &release.GitCommit, &release.SourcePath,
		&release.ComposeYAML, &release.Kind, &release.BuildMode, &release.BuilderImage,
		&release.BuildCommand, &release.RunCommand, &release.ServePath, &release.ListenPort, &imageMapJSON,
		&release.Status, &release.LastError, &created, &completed); err != nil {
		return nil, err
	}
	release.PreviousReleaseID = previousReleaseID.String
	release.ImageMapJSON = imageMapJSON
	if err := json.Unmarshal([]byte(imageMapJSON), &release.ImageMap); err != nil {
		return nil, fmt.Errorf("invalid release image map for %s: %w", release.ID, err)
	}
	if release.ImageMap == nil {
		release.ImageMap = map[string]string{}
	}
	release.CreatedAt = time.Unix(created, 0)
	if completed.Valid {
		release.CompletedAt = time.Unix(completed.Int64, 0)
	}
	if release.BuildMode == "" {
		release.BuildMode = BuildModeCompose
	}
	if release.Kind == "" {
		release.Kind = KindWeb
	}
	return &release, nil
}

// UpdateRelease updates the mutable release record while preserving its ID,
// app ID, and creation timestamp.
func (s *Store) UpdateRelease(ctx context.Context, r *AppRelease) error {
	if r == nil {
		return errors.New("nil app release")
	}
	if r.BuildMode == "" {
		r.BuildMode = BuildModeCompose
	}
	if r.Kind == "" {
		r.Kind = KindWeb
	}
	imageJSON, err := releaseImageJSON(r)
	if err != nil {
		return err
	}
	r.ImageMapJSON = imageJSON
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_releases SET
			previous_release_id=?, source_type=?, source_ref=?, git_ref=?, git_commit=?,
			source_path=?, compose_yaml=?, kind=?, build_mode=?, builder_image=?,
			build_command=?, run_command=?, serve_path=?, listen_port=?, image_map_json=?, status=?,
			last_error=?, completed_at=?
		WHERE id=?`,
		nullableString(r.PreviousReleaseID), r.SourceType, r.SourceRef, r.GitRef, r.GitCommit,
		r.SourcePath, r.ComposeYAML, r.Kind, r.BuildMode, r.BuilderImage,
		r.BuildCommand, r.RunCommand, r.ServePath, r.ListenPort, r.ImageMapJSON, r.Status,
		boundedError(r.LastError), nullableTime(r.CompletedAt), r.ID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *Store) UpdateAppRelease(ctx context.Context, r *AppRelease) error {
	return s.UpdateRelease(ctx, r)
}

func requireAffected(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCurrentRelease points an app at one of its releases atomically with
// the ownership check. apps.current_release_id intentionally has no foreign
// key because it would create a circular SQLite reference with app_releases.
func (s *Store) SetCurrentRelease(ctx context.Context, appID, releaseID string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		var releaseAppID string
		if err := tx.QueryRowContext(ctx, `SELECT app_id FROM app_releases WHERE id=?`, releaseID).Scan(&releaseAppID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if releaseAppID != appID {
			return ErrConflict
		}
		res, err := tx.ExecContext(ctx, `UPDATE apps SET current_release_id=?, updated_at=? WHERE id=?`,
			releaseID, time.Now().Unix(), appID)
		if err != nil {
			return err
		}
		return requireAffected(res)
	})
}

func (s *Store) SetAppCurrentRelease(ctx context.Context, appID, releaseID string) error {
	return s.SetCurrentRelease(ctx, appID, releaseID)
}

func (s *Store) ClearCurrentRelease(ctx context.Context, appID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE apps SET current_release_id=NULL, updated_at=? WHERE id=?`,
		time.Now().Unix(), appID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// ClaimOperation atomically inserts an operation in the preflighting state.
// The partial unique index on app_operations makes this safe across separate
// store instances/processes, not just across goroutines sharing one Store.
func (s *Store) ClaimOperation(ctx context.Context, op *AppOperation) error {
	if op == nil {
		return errors.New("nil app operation")
	}
	now := time.Now()
	if op.ID == "" {
		op.ID = uuid.NewString()
	}
	op.Status = OperationStatusPreflighting
	op.CurrentStep = ""
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM app_operations WHERE app_id=?`, op.AppID).Scan(&op.Attempt); err != nil {
		return err
	}
	op.PreviousReleaseID = ""
	op.CandidateReleaseID = ""
	op.HeartbeatAt = now
	op.Error = ""
	op.CreatedAt = now
	op.StartedAt = time.Time{}
	op.CompletedAt = time.Time{}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_operations (id, app_id, operation_type, status, current_step, attempt,
			previous_release_id, candidate_release_id, heartbeat_at, error, created_at, started_at, completed_at)
		VALUES (?, ?, ?, ?, '', ?, '', '', ?, '', ?, NULL, NULL)`,
		op.ID, op.AppID, op.OperationType, op.Status, op.Attempt, now.Unix(), now.Unix())
	if err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("%w: app already has an active operation", ErrConflict)
		}
		return err
	}
	return nil
}

// ClaimAppOperation is a convenience form for callers that do not already
// have an operation ID.
func (s *Store) ClaimAppOperation(ctx context.Context, appID, operationType string) (*AppOperation, error) {
	op := &AppOperation{AppID: appID, OperationType: operationType}
	if err := s.ClaimOperation(ctx, op); err != nil {
		return nil, err
	}
	return op, nil
}

// FinishOperation records the terminal state and completion time of an
// operation. An empty status means completed.
func (s *Store) FinishOperation(ctx context.Context, id, status, operationErr string) error {
	if status == "" {
		status = OperationStatusCompleted
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_operations
		SET status=?, error=?, completed_at=?
		WHERE id=? AND status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')`,
		status, boundedError(operationErr), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected > 0 {
		return nil
	}
	// Finalization is deliberately idempotent. A late worker cleanup must
	// not overwrite a rollback/completed state written by the deployer.
	var existing string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM app_operations WHERE id=?`, id).Scan(&existing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// SetOperationStatus advances an active operation while refreshing its
// heartbeat. Terminal operations are intentionally not revived.
func (s *Store) SetOperationStatus(ctx context.Context, id, status string) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_operations
		SET status=?, heartbeat_at=?, started_at=COALESCE(started_at, ?)
		WHERE id=? AND status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')`,
		status, now.Unix(), now.Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// SetOperationStep records the coarse step shown by the deployment UI.
func (s *Store) SetOperationStep(ctx context.Context, id, step string) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_operations SET current_step=?, heartbeat_at=?, started_at=COALESCE(started_at, ?)
		WHERE id=? AND status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')`,
		step, now.Unix(), now.Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// InterruptActiveOperations recovers jobs left active by a process crash or
// restart. The deployment page can then offer a deliberate retry instead of
// showing an indefinitely running operation.
func (s *Store) InterruptActiveOperations(ctx context.Context) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, app_id FROM app_operations
			WHERE status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')`)
		if err != nil {
			return err
		}
		type active struct{ id, appID string }
		var jobs []active
		for rows.Next() {
			var job active
			if err := rows.Scan(&job.id, &job.appID); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, job := range jobs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE app_operations SET status=?, error=?, completed_at=?
				WHERE id=?`, OperationStatusInterrupted, "deployment interrupted by service restart", time.Now().Unix(), job.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE apps SET status='error', last_error=?, updated_at=? WHERE id=?`,
				"deployment interrupted by service restart", time.Now().Unix(), job.appID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListRecentOperations returns the newest deployment operations across apps.
func (s *Store) ListRecentOperations(ctx context.Context, limit int) ([]*AppOperation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+appOperationColumns+`
		FROM app_operations ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AppOperation
	for rows.Next() {
		op, err := scanAppOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// DeployStep is one durable phase in a deployment plan.
type DeployStep struct {
	OperationID string    `json:"operation_id"`
	Position    int       `json:"position"`
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

const deployStepColumns = `operation_id, position, step_key, label, status, error, started_at, completed_at`

// CreateDeploySteps replaces the plan for an operation. A deployment is
// immutable once it starts, so replacing a plan is only useful before work
// begins and makes retries explicit and easy to reason about.
func (s *Store) CreateDeploySteps(ctx context.Context, operationID string, steps []DeployStep) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM deploy_steps WHERE operation_id=?`, operationID); err != nil {
			return err
		}
		for i := range steps {
			step := &steps[i]
			step.OperationID = operationID
			if step.Position == 0 {
				step.Position = i + 1
			}
			if step.Status == "" {
				step.Status = "pending"
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO deploy_steps (operation_id, position, step_key, label, status, error, started_at, completed_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				step.OperationID, step.Position, step.Key, step.Label, step.Status, step.Error,
				nullableTime(step.StartedAt), nullableTime(step.CompletedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListDeploySteps(ctx context.Context, operationID string) ([]DeployStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deployStepColumns+`
		FROM deploy_steps WHERE operation_id=? ORDER BY position`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeployStep
	for rows.Next() {
		var step DeployStep
		var started, completed sql.NullInt64
		if err := rows.Scan(&step.OperationID, &step.Position, &step.Key, &step.Label,
			&step.Status, &step.Error, &started, &completed); err != nil {
			return nil, err
		}
		if started.Valid {
			step.StartedAt = time.Unix(started.Int64, 0)
		}
		if completed.Valid {
			step.CompletedAt = time.Unix(completed.Int64, 0)
		}
		out = append(out, step)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDeployStep(ctx context.Context, operationID, key, status, stepErr string) error {
	now := time.Now()
	var completed any
	if status == "succeeded" || status == "failed" || status == "skipped" || status == "rolled_back" {
		completed = now.Unix()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE deploy_steps
		SET status=?, error=?, started_at=CASE WHEN status='pending' THEN ? ELSE started_at END,
			completed_at=?
		WHERE operation_id=? AND step_key=?`,
		status, stepErr, now.Unix(), completed, operationID, key)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// AppendDeployLog stores one output line and returns its monotonically
// increasing per-operation sequence.
func (s *Store) AppendDeployLog(ctx context.Context, operationID, step, level, message string) (int, error) {
	message = boundedError(message)
	if message == "" {
		return 0, nil
	}
	var sequence int
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(sequence), 0) + 1 FROM deploy_logs WHERE operation_id=?`, operationID).Scan(&sequence); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO deploy_logs (operation_id, sequence, step_key, level, message, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, operationID, sequence, step, level, message, time.Now().Unix())
		return err
	})
	return sequence, err
}

// DeployLog is a durable line in a deployment's build/output feed.
type DeployLog struct {
	OperationID string    `json:"operation_id"`
	Sequence    int       `json:"sequence"`
	Step        string    `json:"step,omitempty"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) ListDeployLogs(ctx context.Context, operationID string, after, limit int) ([]DeployLog, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT operation_id, sequence, step_key, level, message, created_at
		FROM deploy_logs WHERE operation_id=? AND sequence>?
		ORDER BY sequence LIMIT ?`, operationID, after, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DeployLog
	latest := after
	if latest < 0 {
		latest = 0
	}
	for rows.Next() {
		var line DeployLog
		var created int64
		if err := rows.Scan(&line.OperationID, &line.Sequence, &line.Step, &line.Level, &line.Message, &created); err != nil {
			return nil, 0, err
		}
		line.CreatedAt = time.Unix(created, 0)
		if line.Sequence > latest {
			latest = line.Sequence
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Close the result set before asking for the high-water mark. The store
	// intentionally has one SQLite connection; leaving rows open while
	// issuing the second query deadlocks long feeds at the page limit.
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) FROM deploy_logs WHERE operation_id=?`, operationID).Scan(&latest); err != nil {
		return nil, 0, err
	}
	return out, latest, nil
}

func (s *Store) ActiveOperation(ctx context.Context, appID string) (*AppOperation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appOperationColumns+`
		FROM app_operations WHERE app_id=? AND status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, appID)
	op, err := scanAppOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return op, err
}

func (s *Store) HasActiveOperation(ctx context.Context, appID string) (bool, error) {
	_, err := s.ActiveOperation(ctx, appID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) GetOperation(ctx context.Context, id string) (*AppOperation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appOperationColumns+` FROM app_operations WHERE id=?`, id)
	op, err := scanAppOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return op, err
}

func (s *Store) GetAppOperation(ctx context.Context, id string) (*AppOperation, error) {
	return s.GetOperation(ctx, id)
}

func (s *Store) ListOperations(ctx context.Context, appID string) ([]*AppOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appOperationColumns+`
		FROM app_operations WHERE app_id=? ORDER BY created_at DESC, rowid DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AppOperation
	for rows.Next() {
		op, err := scanAppOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *Store) ListAppOperations(ctx context.Context, appID string) ([]*AppOperation, error) {
	return s.ListOperations(ctx, appID)
}

// HeartbeatOperation advances the liveness timestamp for an active operation.
// Completed or failed operations cannot be revived by a late heartbeat.
func (s *Store) HeartbeatOperation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE app_operations SET heartbeat_at=?
		WHERE id=? AND status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back')`,
		time.Now().Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func scanAppOperation(r rowScanner) (*AppOperation, error) {
	var op AppOperation
	var heartbeat, started, completed sql.NullInt64
	var created int64
	var currentStep, previousReleaseID, candidateReleaseID string
	var attempt int
	if err := r.Scan(&op.ID, &op.AppID, &op.OperationType, &op.Status, &currentStep, &attempt,
		&previousReleaseID, &candidateReleaseID, &heartbeat, &op.Error, &created, &started, &completed); err != nil {
		return nil, err
	}
	op.CurrentStep = currentStep
	op.Attempt = attempt
	op.PreviousReleaseID = previousReleaseID
	op.CandidateReleaseID = candidateReleaseID
	if heartbeat.Valid {
		op.HeartbeatAt = time.Unix(heartbeat.Int64, 0)
	}
	op.CreatedAt = time.Unix(created, 0)
	if started.Valid {
		op.StartedAt = time.Unix(started.Int64, 0)
	}
	if completed.Valid {
		op.CompletedAt = time.Unix(completed.Int64, 0)
	}
	return &op, nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
