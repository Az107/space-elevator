package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// APIToken is a Personal Access Token for the REST API. Only the
// SHA-256 hash of the raw token is persisted; the raw value is shown
// exactly once at creation (MintAPIToken).
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	Prefix     string     `json:"prefix"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// tokenRawPrefix is the fixed marker that makes tokens recognizable in
// logs/configs without leaking the secret part.
const tokenRawPrefix = "se_"

// tokenPrefixLen is how many characters of the raw token are stored
// for display in lists (including the se_ marker).
const tokenPrefixLen = 12

func hashAPIToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// MintAPIToken creates a token and returns it together with the raw
// secret. The raw value is never stored or logged; if the caller loses
// it, the token must be revoked and re-created.
func (s *Store) MintAPIToken(ctx context.Context, name string, expiresAt *time.Time) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("token name required")
	}
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return nil, "", fmt.Errorf("generate token: %w", err)
	}
	raw := tokenRawPrefix + base64.RawURLEncoding.EncodeToString(rawBytes)

	t := &APIToken{
		ID:        uuid.NewString(),
		Name:      name,
		TokenHash: hashAPIToken(raw),
		Prefix:    raw[:tokenPrefixLen],
		ExpiresAt: expiresAt,
	}
	now := time.Now()
	t.CreatedAt = now
	var exp, used any
	if t.ExpiresAt != nil {
		exp = t.ExpiresAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (id, name, token_hash, prefix, expires_at, last_used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenHash, t.Prefix, exp, used, t.CreatedAt.Unix())
	if err != nil {
		return nil, "", err
	}
	return t, raw, nil
}

// GetAPIToken resolves a raw bearer token. Expired tokens return
// ErrNotFound so they behave exactly like unknown tokens.
func (s *Store) GetAPIToken(ctx context.Context, raw string) (*APIToken, error) {
	if !strings.HasPrefix(raw, tokenRawPrefix) {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, prefix, expires_at, last_used_at, created_at
		 FROM api_tokens WHERE token_hash=?`, hashAPIToken(raw))
	t, err := scanAPIToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return t, nil
}

// TouchAPIToken records usage. Errors are non-fatal for the request
// path; callers ignore the return value.
func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, at.Unix(), id)
	return err
}

func (s *Store) ListAPITokens(ctx context.Context) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_hash, prefix, expires_at, last_used_at, created_at
		FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id)
	return err
}

func scanAPIToken(r rowScanner) (*APIToken, error) {
	var t APIToken
	var exp, used, created sql.NullInt64
	if err := r.Scan(&t.ID, &t.Name, &t.TokenHash, &t.Prefix, &exp, &used, &created); err != nil {
		return nil, err
	}
	if exp.Valid {
		e := time.Unix(exp.Int64, 0)
		t.ExpiresAt = &e
	}
	if used.Valid {
		u := time.Unix(used.Int64, 0)
		t.LastUsedAt = &u
	}
	t.CreatedAt = time.Unix(created.Int64, 0)
	return &t, nil
}
