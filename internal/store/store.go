package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The DB holds password hashes and git tokens — keep it private.
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(abs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlinked database %q", abs)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("database path is a directory: %q", abs)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", abs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	// SQLite defaults to 0644 for the main file and WAL/journal siblings.
	for _, f := range []string{abs, abs + "-wal", abs + "-shm", abs + "-journal"} {
		if err := os.Chmod(f, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, fmt.Errorf("secure database file %s: %w", f, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, f := range []string{abs, abs + "-wal", abs + "-shm", abs + "-journal"} {
		if err := os.Chmod(f, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, fmt.Errorf("secure database file %s: %w", f, err)
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	// Track applied migrations; the SQL files themselves are not
	// idempotent (ALTER TABLE) and must run exactly once.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var applied int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, e.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if err := s.applyMigration(e.Name(), b); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *Store) applyMigration(name string, body []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(string(body)); err != nil {
		_ = tx.Rollback()
		// Older development builds could apply a multi-statement file
		// without recording it. Only accept that recovery when the complete
		// target shape is present; otherwise leave the error visible instead
		// of marking a half-applied migration as successful.
		if isAlreadyAppliedError(err) {
			complete, checkErr := migrationAlreadyApplied(s.db, name)
			if checkErr != nil {
				return checkErr
			}
			if complete {
				_, recordErr := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, time.Now().Unix())
				if recordErr != nil {
					return fmt.Errorf("record recovered migration: %w", recordErr)
				}
				return nil
			}
		}
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, time.Now().Unix()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration: %w", err)
	}
	return tx.Commit()
}

func isAlreadyAppliedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column name") || strings.Contains(message, "already exists")
}

func migrationAlreadyApplied(db *sql.DB, name string) (bool, error) {
	hasTable := func(table string) (bool, error) {
		var found string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	hasColumn := func(table, column string) (bool, error) {
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				return false, err
			}
			if name == column {
				return true, rows.Err()
			}
		}
		return false, rows.Err()
	}
	allColumns := func(table string, columns ...string) (bool, error) {
		for _, column := range columns {
			ok, err := hasColumn(table, column)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	}

	switch name {
	case "0001_init.sql":
		for _, table := range []string{"apps", "app_domains", "git_credentials", "users", "sessions"} {
			ok, err := hasTable(table)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	case "0002_session_hardening.sql":
		ok, err := allColumns("sessions", "last_seen_at")
		if err != nil || !ok {
			return ok, err
		}
		return allColumns("apps", "last_error")
	case "0003_app_secrets.sql":
		return hasTable("app_secrets")
	case "0004_custom_build.sql":
		return allColumns("apps", "build_mode", "builder_image", "build_command", "run_command", "listen_port")
	case "0005_api_tokens.sql":
		return hasTable("api_tokens")
	case "0006_app_runtime_slug.sql":
		return allColumns("apps", "slug")
	case "0006_remove_api_tokens.sql":
		ok, err := hasTable("api_tokens")
		return !ok, err
	case "0007_app_kind.sql":
		return allColumns("apps", "kind", "runtime", "runtime_version", "entrypoint", "scale_to_zero", "idle_timeout", "last_invoked_at")
	case "0008_audit_log.sql":
		return hasTable("audit_events")
	case "0009_app_updates.sql":
		if ok, err := allColumns("apps", "current_release_id"); err != nil || !ok {
			return ok, err
		}
		for _, table := range []string{"app_storage", "app_releases", "app_operations"} {
			ok, err := hasTable(table)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	case "0010_deployment_steps.sql":
		if ok, err := allColumns("app_operations", "current_step", "started_at", "attempt", "candidate_release_id", "previous_release_id"); err != nil || !ok {
			return ok, err
		}
		for _, table := range []string{"deploy_steps", "deploy_logs"} {
			ok, err := hasTable(table)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	case "0011_web_build_path.sql":
		ok, err := allColumns("apps", "serve_path")
		if err != nil || !ok {
			return ok, err
		}
		return allColumns("app_releases", "serve_path")
	default:
		// CREATE-only migrations are safe to recover when all their tables
		// are present; unknown future migrations should fail closed.
		return false, nil
	}
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
