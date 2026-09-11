package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is the stable runtime identity (container/network/image names,
	// Traefik route file, container label). It defaults to Name at
	// creation and does not change when the app is renamed, so runtime
	// artifacts and routes stay valid across a rename.
	Slug        string            `json:"-"`
	SourceType  string            `json:"source_type"`
	SourceRef   string            `json:"source_ref"`
	GitRef      string            `json:"git_ref"`
	DropKind    string            `json:"drop_kind"`
	ComposeYAML string            `json:"compose_yaml"`
	Env         map[string]string `json:"env"`
	Status      string            `json:"status"`
	LastError   string            `json:"last_error,omitempty"`
	Domains     []string          `json:"domains"`
	// Custom build mode ("advanced deploy"): when BuildMode is
	// "custom", the repo has no compose file and the platform
	// synthesizes a single-stage Dockerfile from the fields below.
	// "compose" (default) means ComposeYAML is authoritative.
	BuildMode    string `json:"build_mode"`
	BuilderImage string `json:"builder_image,omitempty"`
	BuildCommand string `json:"build_command,omitempty"`
	RunCommand   string `json:"run_command,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Build mode values.
const (
	BuildModeCompose = "compose"
	BuildModeCustom  = "custom"
)

var ErrNotFound = errors.New("not found")

// appColumns is the canonical SELECT list for app rows.
const appColumns = `id, name, slug, source_type, source_ref, git_ref, drop_kind,
	compose_yaml, env_json, status, last_error,
	build_mode, builder_image, build_command, run_command, listen_port,
	created_at, updated_at`

func (s *Store) CreateApp(ctx context.Context, a *App) error {
	a.CreatedAt = time.Now()
	a.UpdatedAt = a.CreatedAt
	env, _ := json.Marshal(a.Env)
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
	}
	if a.Slug == "" {
		a.Slug = a.Name
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO apps (id, name, slug, source_type, source_ref, git_ref, drop_kind,
		                  compose_yaml, env_json, status, last_error,
		                  build_mode, builder_image, build_command, run_command, listen_port,
		                  created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.Slug, a.SourceType, a.SourceRef, a.GitRef, a.DropKind,
		a.ComposeYAML, string(env), a.Status, a.LastError,
		a.BuildMode, a.BuilderImage, a.BuildCommand, a.RunCommand, a.ListenPort,
		a.CreatedAt.Unix(), a.UpdatedAt.Unix())
	return err
}

// UpdateAppName changes the display name only. The slug (runtime identity)
// is deliberately left untouched so containers, routes, and images keep
// resolving after a rename.
func (s *Store) UpdateAppName(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET name=?, updated_at=? WHERE id=?`,
		name, time.Now().Unix(), id)
	return err
}

func (s *Store) UpdateAppStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().Unix(), id)
	return err
}

// UpdateAppStatusErr records a status plus the human-readable failure
// reason shown on the app detail page.
func (s *Store) UpdateAppStatusErr(ctx context.Context, id, status, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET status=?, last_error=?, updated_at=? WHERE id=?`,
		status, lastErr, time.Now().Unix(), id)
	return err
}

func (s *Store) ClearAppError(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET last_error='' WHERE id=?`, id)
	return err
}

func (s *Store) UpdateApp(ctx context.Context, a *App) error {
	a.UpdatedAt = time.Now()
	env, _ := json.Marshal(a.Env)
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET source_ref=?, git_ref=?, compose_yaml=?, env_json=?, status=?,
			build_mode=?, builder_image=?, build_command=?, run_command=?, listen_port=?,
			updated_at=?
		WHERE id=?`,
		a.SourceRef, a.GitRef, a.ComposeYAML, string(env), a.Status,
		a.BuildMode, a.BuilderImage, a.BuildCommand, a.RunCommand, a.ListenPort,
		a.UpdatedAt.Unix(), a.ID)
	return err
}

func (s *Store) GetApp(ctx context.Context, id string) (*App, error) {
	return s.queryApp(ctx, "SELECT "+appColumns+" FROM apps WHERE id=?", id)
}

func (s *Store) GetAppByName(ctx context.Context, name string) (*App, error) {
	return s.queryApp(ctx, "SELECT "+appColumns+" FROM apps WHERE name=?", name)
}

func (s *Store) ListApps(ctx context.Context) ([]*App, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+appColumns+" FROM apps ORDER BY created_at DESC")
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
		if _, err := tx.Exec(`DELETE FROM app_domains WHERE app_id=?`, appID); err != nil {
			return err
		}
		for _, d := range domains {
			if _, err := tx.Exec(`INSERT INTO app_domains(app_id, domain) VALUES (?, ?)`, appID, d); err != nil {
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
	var created, updated int64
	err := r.Scan(&a.ID, &a.Name, &a.Slug, &a.SourceType, &a.SourceRef, &a.GitRef, &a.DropKind,
		&a.ComposeYAML, &env, &a.Status, &a.LastError,
		&a.BuildMode, &a.BuilderImage, &a.BuildCommand, &a.RunCommand, &a.ListenPort,
		&created, &updated)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(env), &a.Env)
	if a.Env == nil {
		a.Env = map[string]string{}
	}
	if a.BuildMode == "" {
		a.BuildMode = BuildModeCompose
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
