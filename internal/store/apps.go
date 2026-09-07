package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type App struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	SourceType  string            `json:"source_type"`
	SourceRef   string            `json:"source_ref"`
	GitRef      string            `json:"git_ref"`
	DropKind    string            `json:"drop_kind"`
	ComposeYAML string            `json:"compose_yaml"`
	Env         map[string]string `json:"env"`
	Status      string            `json:"status"`
	Domains     []string          `json:"domains"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

var ErrNotFound = errors.New("not found")

func (s *Store) CreateApp(ctx context.Context, a *App) error {
	a.CreatedAt = time.Now()
	a.UpdatedAt = a.CreatedAt
	env, _ := json.Marshal(a.Env)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO apps (id, name, source_type, source_ref, git_ref, drop_kind,
		                  compose_yaml, env_json, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.SourceType, a.SourceRef, a.GitRef, a.DropKind,
		a.ComposeYAML, string(env), a.Status,
		a.CreatedAt.Unix(), a.UpdatedAt.Unix())
	return err
}

func (s *Store) UpdateAppStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().Unix(), id)
	return err
}

func (s *Store) UpdateApp(ctx context.Context, a *App) error {
	a.UpdatedAt = time.Now()
	env, _ := json.Marshal(a.Env)
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET source_ref=?, git_ref=?, compose_yaml=?, env_json=?, status=?, updated_at=?
		WHERE id=?`,
		a.SourceRef, a.GitRef, a.ComposeYAML, string(env), a.Status, a.UpdatedAt.Unix(), a.ID)
	return err
}

func (s *Store) GetApp(ctx context.Context, id string) (*App, error) {
	return s.queryApp(ctx, "SELECT id, name, source_type, source_ref, git_ref, drop_kind, compose_yaml, env_json, status, created_at, updated_at FROM apps WHERE id=?", id)
}

func (s *Store) GetAppByName(ctx context.Context, name string) (*App, error) {
	return s.queryApp(ctx, "SELECT id, name, source_type, source_ref, git_ref, drop_kind, compose_yaml, env_json, status, created_at, updated_at FROM apps WHERE name=?", name)
}

func (s *Store) ListApps(ctx context.Context) ([]*App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, source_type, source_ref, git_ref, drop_kind, compose_yaml, env_json, status, created_at, updated_at FROM apps ORDER BY created_at DESC`)
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
		SELECT a.id, a.name, a.source_type, a.source_ref, a.git_ref, a.drop_kind,
		       a.compose_yaml, a.env_json, a.status, a.created_at, a.updated_at
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
	err := r.Scan(&a.ID, &a.Name, &a.SourceType, &a.SourceRef, &a.GitRef, &a.DropKind,
		&a.ComposeYAML, &env, &a.Status, &created, &updated)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(env), &a.Env)
	if a.Env == nil {
		a.Env = map[string]string{}
	}
	a.CreatedAt = time.Unix(created, 0)
	a.UpdatedAt = time.Unix(updated, 0)
	return &a, nil
}