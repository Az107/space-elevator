package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type User struct {
	ID           string
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

// Session.ID is the raw cookie token in memory; only its SHA-256 hash is
// persisted, so a leaked database cannot be replayed as valid sessions.
type Session struct {
	ID         string
	UserID     string
	ExpiresAt  time.Time
	LastSeenAt time.Time
	CreatedAt  time.Time
}

func hashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt.Unix())
	return err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE username=?`,
		username).Scan(&u.ID, &u.Username, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, password_hash, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		var created int64
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0)
		out = append(out, &u)
	}
	return out, rows.Err()
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE id=?`,
		id).Scan(&u.ID, &u.Username, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

// ErrUsernameTaken is returned by UpdateUsername when the new username
// collides with an existing account.
var ErrUsernameTaken = errors.New("username already taken")

// UpdateUserPassword replaces the stored bcrypt hash. The caller is
// responsible for hashing; raw passwords never reach the store.
func (s *Store) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, passwordHash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUsername renames an account. Uniqueness is enforced by the
// schema; the collision is surfaced as ErrUsernameTaken.
func (s *Store) UpdateUsername(ctx context.Context, userID, username string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET username=? WHERE id=?`, username, userID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrUsernameTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	now := time.Now()
	sess.CreatedAt = now
	if sess.LastSeenAt.IsZero() {
		sess.LastSeenAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		hashSessionToken(sess.ID), sess.UserID, sess.ExpiresAt.Unix(), sess.LastSeenAt.Unix(), sess.CreatedAt.Unix())
	return err
}

// GetSession looks up a session by its raw cookie token.
func (s *Store) GetSession(ctx context.Context, rawToken string) (*Session, error) {
	var sess Session
	var expires, lastSeen, created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, expires_at, last_seen_at, created_at FROM sessions WHERE id=?`, hashSessionToken(rawToken)).
		Scan(&sess.ID, &sess.UserID, &expires, &lastSeen, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ID = rawToken
	sess.ExpiresAt = time.Unix(expires, 0)
	sess.LastSeenAt = time.Unix(lastSeen, 0)
	sess.CreatedAt = time.Unix(created, 0)
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, rawToken string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, hashSessionToken(rawToken))
	return err
}

// TouchSession slides the idle window forward.
func (s *Store) TouchSession(ctx context.Context, rawToken string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at=? WHERE id=?`, at.Unix(), hashSessionToken(rawToken))
	return err
}

func (s *Store) DeleteSessionsForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// DeleteOtherSessionsForUser invalidates every session of the user
// except the one identified by keepRawToken (the current browser's
// session), so a password change can't log the actor out mid-flight.
func (s *Store) DeleteOtherSessionsForUser(ctx context.Context, userID, keepRawToken string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=? AND id != ?`,
		userID, hashSessionToken(keepRawToken))
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`,
		time.Now().Unix(), time.Now().Add(-30*24*time.Hour).Unix())
	return err
}
