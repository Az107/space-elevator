package store

import (
	"context"
	"database/sql"
	"embed"
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
	abs, _ := filepath.Abs(path)
	// The DB holds password hashes and git tokens — keep it private.
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
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
		_ = os.Chmod(f, 0o600)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
		if _, err := s.db.Exec(string(b)); err != nil {
			// SQLite has no ALTER TABLE ... IF NOT EXISTS. A migration may
			// have been applied by an intermediate build that predated
			// schema_migrations tracking; a duplicate-column error means
			// the schema is already in the target shape, so record it and
			// move on instead of refusing to start forever after.
			if strings.Contains(err.Error(), "duplicate column name") {
				if _, rerr := s.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, e.Name(), time.Now().Unix()); rerr != nil {
					return fmt.Errorf("record migration %s: %w", e.Name(), rerr)
				}
				continue
			}
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, e.Name(), time.Now().Unix()); err != nil {
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}
	}
	return nil
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
