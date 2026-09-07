package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type GitCredential struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Username  string    `json:"username"`
	Token     string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) UpsertCredential(ctx context.Context, c *GitCredential) error {
	if c.ID == "" {
		return errors.New("id required")
	}
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO git_credentials (id, host, username, token, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(host) DO UPDATE SET
			username=excluded.username,
			token=excluded.token,
			updated_at=excluded.updated_at`,
		c.ID, c.Host, c.Username, c.Token, c.CreatedAt.Unix(), c.UpdatedAt.Unix())
	return err
}

func (s *Store) GetCredentialForHost(ctx context.Context, host string) (*GitCredential, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, host, username, token, created_at, updated_at FROM git_credentials WHERE host=?`, host)
	var c GitCredential
	var created, updated int64
	err := row.Scan(&c.ID, &c.Host, &c.Username, &c.Token, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(created, 0)
	c.UpdatedAt = time.Unix(updated, 0)
	return &c, nil
}

func (s *Store) ListCredentials(ctx context.Context) ([]*GitCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, host, username, token, created_at, updated_at FROM git_credentials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GitCredential
	for rows.Next() {
		var c GitCredential
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Host, &c.Username, &c.Token, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0)
		c.UpdatedAt = time.Unix(updated, 0)
		out = append(out, &c)
	}
	return out, rows.Err()
}