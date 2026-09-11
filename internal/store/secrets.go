package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Env var keys are restricted to the shell-safe subset so they can't
// confuse compose YAML, container runtimes, or downstream tooling.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvKey reports whether k is an acceptable environment variable
// name ([A-Za-z_][A-Za-z0-9_]*).
func ValidEnvKey(k string) bool {
	return envKeyPattern.MatchString(k)
}

// ParseKVLines parses a "KEY=VALUE" per line block (as submitted by the
// deploy form textareas). It returns the pairs that parsed, plus the
// offending line descriptions so the caller can surface them. The last
// occurrence of a duplicated key wins, matching shell semantics.
func ParseKVLines(block string) (map[string]string, []string) {
	var bad []string
	out := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !ValidEnvKey(k) {
			bad = append(bad, line)
			continue
		}
		out[k] = v
	}
	return out, bad
}

// ParseKVArgs parses repeatable "KEY=VALUE" CLI flag values.
func ParseKVArgs(args []string) (map[string]string, []string) {
	var bad []string
	out := map[string]string{}
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok || !ValidEnvKey(strings.TrimSpace(k)) {
			bad = append(bad, arg)
			continue
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, bad
}

// SetSecret upserts one secret value for an app.
func (s *Store) SetSecret(ctx context.Context, appID, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_secrets (app_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(app_id, key) DO UPDATE SET value=excluded.value`,
		appID, key, value)
	return err
}

// DeleteSecret removes one secret. Deleting an unknown key is not an error.
func (s *Store) DeleteSecret(ctx context.Context, appID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_secrets WHERE app_id=? AND key=?`, appID, key)
	return err
}

// ListSecretKeys returns the secret key names for an app. Values are
// deliberately not exposed here: secrets are write-only everywhere
// except LoadRuntimeEnv.
func (s *Store) ListSecretKeys(ctx context.Context, appID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM app_secrets WHERE app_id=? ORDER BY key`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetSecrets returns the raw secret values for an app. Only the runtime
// injection path (LoadRuntimeEnv) and nothing user-facing should call
// this.
func (s *Store) GetSecrets(ctx context.Context, appID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM app_secrets WHERE app_id=?`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// LoadRuntimeEnv merges an app's plain env with its secret values for
// injection into containers. Secrets override plain env. This is the
// single path through which secret values are ever readable.
func (s *Store) LoadRuntimeEnv(ctx context.Context, appID string, base map[string]string) (map[string]string, error) {
	if base == nil {
		base = map[string]string{}
	}
	secrets, err := s.GetSecrets(ctx, appID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return base, nil
		}
		return nil, err
	}
	for k, v := range secrets {
		base[k] = v
	}
	return base, nil
}

// UpdateAppEnv replaces the app's plain env map (env_json column).
func (s *Store) UpdateAppEnv(ctx context.Context, appID string, env map[string]string) error {
	if env == nil {
		env = map[string]string{}
	}
	b, _ := json.Marshal(env)
	_, err := s.db.ExecContext(ctx, `
		UPDATE apps SET env_json=?, updated_at=? WHERE id=?`,
		string(b), time.Now().Unix(), appID)
	return err
}
