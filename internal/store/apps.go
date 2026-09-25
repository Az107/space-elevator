package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is the stable runtime identity (container/network/image names,
	// Traefik route file, container label). It defaults to Name at
	// creation and does not change when the app is renamed, so runtime
	// artifacts and routes stay valid across a rename.
	Slug             string            `json:"-"`
	SourceType       string            `json:"source_type"`
	SourceRef        string            `json:"source_ref"`
	GitRef           string            `json:"git_ref"`
	DropKind         string            `json:"drop_kind"`
	ComposeYAML      string            `json:"compose_yaml"`
	Env              map[string]string `json:"env"`
	CurrentReleaseID string            `json:"current_release_id,omitempty"`
	Status           string            `json:"status"`
	LastError        string            `json:"last_error,omitempty"`
	Domains          []string          `json:"domains"`
	// Custom build mode ("advanced deploy"): when BuildMode is
	// "custom", the repo has no compose file and the platform
	// synthesizes a single-stage Dockerfile from the fields below.
	// "static" builds a web app in a tool image and serves ServePath with
	// the platform's Nginx image. "compose" (default) means ComposeYAML
	// is authoritative.
	BuildMode    string `json:"build_mode"`
	BuilderImage string `json:"builder_image,omitempty"`
	BuildCommand string `json:"build_command,omitempty"`
	RunCommand   string `json:"run_command,omitempty"`
	ServePath    string `json:"serve_path,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
	// Kind classifies the workload: "web" (HTTP app/compose/static),
	// "function" (adapter-wrapped handler), or "custom" (bring-your-own
	// container/Dockerfile). It is orthogonal to SourceType.
	Kind string `json:"kind"`
	// Runtime/RuntimeVersion/Entrypoint describe function workloads:
	// language ("python"|"node"), the version tag for the base image
	// ("3.12"|"20"), and the handler location ("handler.py:handler").
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
	Entrypoint     string `json:"entrypoint,omitempty"`
	// Scale-to-zero fields are stored now but inert until the activator
	// lands; they future-proof the schema and the deploy labels.
	ScaleToZero   bool      `json:"scale_to_zero,omitempty"`
	IdleTimeout   int       `json:"idle_timeout,omitempty"`
	LastInvokedAt int64     `json:"last_invoked_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Build mode values.
const (
	BuildModeCompose = "compose"
	BuildModeCustom  = "custom"
	BuildModeStatic  = "static"
)

// Kind values.
const (
	KindWeb      = "web"
	KindFunction = "function"
	KindCustom   = "custom"
)

var ErrNotFound = errors.New("not found")

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// appColumns is the canonical SELECT list for app rows.
const appColumns = `id, name, slug, source_type, source_ref, git_ref, drop_kind,
	compose_yaml, env_json, current_release_id, status, last_error,
	build_mode, builder_image, build_command, run_command, serve_path, listen_port,
	kind, runtime, runtime_version, entrypoint, scale_to_zero, idle_timeout, last_invoked_at,
	created_at, updated_at`

func (s *Store) CreateApp(ctx context.Context, a *App) error {
	a.CreatedAt = time.Now()
	a.UpdatedAt = a.CreatedAt
	for key, value := range a.Env {
		if !ValidEnvKey(key) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment entry %q", key)
		}
	}
	env, _ := json.Marshal(a.Env)
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
	}
	if a.Kind == "" {
		a.Kind = KindWeb
	}
	if a.Slug == "" {
		a.Slug = a.Name
	}
	var currentReleaseID any
	if a.CurrentReleaseID != "" {
		currentReleaseID = a.CurrentReleaseID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO apps (id, name, slug, source_type, source_ref, git_ref, drop_kind,
		                  compose_yaml, env_json, current_release_id, status, last_error,
		                  build_mode, builder_image, build_command, run_command, serve_path, listen_port,
		                  kind, runtime, runtime_version, entrypoint, scale_to_zero, idle_timeout, last_invoked_at,
		                  created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.Slug, a.SourceType, a.SourceRef, a.GitRef, a.DropKind,
		a.ComposeYAML, string(env), currentReleaseID, a.Status, a.LastError,
		a.BuildMode, a.BuilderImage, a.BuildCommand, a.RunCommand, a.ServePath, a.ListenPort,
		a.Kind, a.Runtime, a.RuntimeVersion, a.Entrypoint, boolToInt(a.ScaleToZero), a.IdleTimeout, a.LastInvokedAt,
		a.CreatedAt.Unix(), a.UpdatedAt.Unix())
	return err
}

// UpdateAppName changes the display name only. The slug (runtime identity)
// is deliberately left untouched so containers, routes, and images keep
// resolving after a rename.
func (s *Store) UpdateAppName(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE apps SET name=?, updated_at=? WHERE id=?`,
		name, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *Store) UpdateAppStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE apps SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// UpdateAppStatusErr records a status plus the human-readable failure
// reason shown on the app detail page.
func (s *Store) UpdateAppStatusErr(ctx context.Context, id, status, lastErr string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE apps SET status=?, last_error=?, updated_at=? WHERE id=?`,
		status, boundedError(lastErr), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *Store) ClearAppError(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE apps SET last_error='' WHERE id=?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *Store) UpdateApp(ctx context.Context, a *App) error {
	a.UpdatedAt = time.Now()
	for key, value := range a.Env {
		if !ValidEnvKey(key) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment entry %q", key)
		}
	}
	env, _ := json.Marshal(a.Env)
	var currentReleaseID any
	if a.CurrentReleaseID != "" {
		currentReleaseID = a.CurrentReleaseID
	}
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
	}
	if a.Kind == "" {
		a.Kind = KindWeb
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE apps SET source_ref=?, git_ref=?, drop_kind=?, compose_yaml=?, env_json=?, current_release_id=?, status=?,
			build_mode=?, builder_image=?, build_command=?, run_command=?, serve_path=?, listen_port=?,
			kind=?, runtime=?, runtime_version=?, entrypoint=?, scale_to_zero=?, idle_timeout=?,
			updated_at=?
		WHERE id=?`,
		a.SourceRef, a.GitRef, a.DropKind, a.ComposeYAML, string(env), currentReleaseID, a.Status,
		a.BuildMode, a.BuilderImage, a.BuildCommand, a.RunCommand, a.ServePath, a.ListenPort,
		a.Kind, a.Runtime, a.RuntimeVersion, a.Entrypoint, boolToInt(a.ScaleToZero), a.IdleTimeout,
		a.UpdatedAt.Unix(), a.ID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *Store) GetApp(ctx context.Context, id string) (*App, error) {
	return s.queryApp(ctx, "SELECT "+appColumns+" FROM apps WHERE id=?", id)
}

func (s *Store) GetAppByName(ctx context.Context, name string) (*App, error) {
	return s.queryApp(ctx, "SELECT "+appColumns+" FROM apps WHERE name=?", name)
}

func (s *Store) ListApps(ctx context.Context) ([]*App, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+appColumns+" FROM apps ORDER BY created_at DESC, rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteApp(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id=?`, id)
	return err
}

func (s *Store) SetAppDomains(ctx context.Context, appID string, domains []string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_domains WHERE app_id=?`, appID); err != nil {
			return err
		}
		for _, d := range domains {
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_domains(app_id, domain) VALUES (?, ?)`, appID, d); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetAppDomains(ctx context.Context, appID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain FROM app_domains WHERE app_id=? ORDER BY domain`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) ListDomain(ctx context.Context, domain string) (*App, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+appColumns+`
		FROM apps a JOIN app_domains d ON d.app_id=a.id
		WHERE d.domain=?`, domain)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) queryApp(ctx context.Context, q string, args ...any) (*App, error) {
	row := s.db.QueryRowContext(ctx, q, args...)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func scanApp(r rowScanner) (*App, error) {
	var a App
	var env string
	var currentReleaseID sql.NullString
	var created, updated int64
	var scaleToZero int
	err := r.Scan(&a.ID, &a.Name, &a.Slug, &a.SourceType, &a.SourceRef, &a.GitRef, &a.DropKind,
		&a.ComposeYAML, &env, &currentReleaseID, &a.Status, &a.LastError,
		&a.BuildMode, &a.BuilderImage, &a.BuildCommand, &a.RunCommand, &a.ServePath, &a.ListenPort,
		&a.Kind, &a.Runtime, &a.RuntimeVersion, &a.Entrypoint, &scaleToZero, &a.IdleTimeout, &a.LastInvokedAt,
		&created, &updated)
	if err != nil {
		return nil, err
	}
	a.ScaleToZero = scaleToZero != 0
	a.CurrentReleaseID = currentReleaseID.String
	if err := json.Unmarshal([]byte(env), &a.Env); err != nil {
		return nil, fmt.Errorf("invalid environment JSON for %s: %w", a.ID, err)
	}
	if a.Env == nil {
		a.Env = map[string]string{}
	}
	for key, value := range a.Env {
		if !ValidEnvKey(key) || strings.ContainsRune(value, 0) {
			delete(a.Env, key)
		}
	}
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
	}
	if a.Kind == "" {
		a.Kind = KindWeb
	}
	// Rows written before the slug column existed, or a row created
	// without an explicit slug, fall back to the name.
	if a.Slug == "" {
		a.Slug = a.Name
	}
	a.CreatedAt = time.Unix(created, 0)
	a.UpdatedAt = time.Unix(updated, 0)
	return &a, nil
}
