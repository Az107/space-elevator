package store

import (
	"context"
	"database/sql"
	"time"
)

// AuditEvent is one append-only record of a security-relevant action.
// Secret and password values are never stored here.
type AuditEvent struct {
	ID         int64
	At         time.Time
	ActorType  string
	ActorID    string
	ActorLabel string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	Outcome    string
	IP         string
	UserAgent  string
	Detail     string
}

// RecordAudit appends one event. Callers treat audit failures as
// non-fatal (log to stderr instead), so the request path is never
// blocked by the audit table.
func (s *Store) RecordAudit(ctx context.Context, e *AuditEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events
			(at, actor_type, actor_id, actor_label, action,
			 target_type, target_id, target_name, outcome, ip, user_agent, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.At.Unix(), e.ActorType, e.ActorID, e.ActorLabel, e.Action,
		e.TargetType, e.TargetID, e.TargetName, e.Outcome, e.IP, e.UserAgent, e.Detail)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		e.ID = id
	}
	return nil
}

// AuditFilter narrows ListAudit. Zero values mean "no constraint".
type AuditFilter struct {
	Action    string
	Outcome   string
	ActorType string
	Target    string
	Since     time.Time
	Limit     int
}

// ListAudit returns events newest-first.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]*AuditEvent, error) {
	q := `SELECT id, at, actor_type, actor_id, actor_label, action,
	             target_type, target_id, target_name, outcome, ip, user_agent, detail
	      FROM audit_events WHERE 1=1`
	var args []any
	if f.Action != "" {
		q += " AND action=?"
		args = append(args, f.Action)
	}
	if f.Outcome != "" {
		q += " AND outcome=?"
		args = append(args, f.Outcome)
	}
	if f.ActorType != "" {
		q += " AND actor_type=?"
		args = append(args, f.ActorType)
	}
	if f.Target != "" {
		q += " AND target_name=?"
		args = append(args, f.Target)
	}
	if !f.Since.IsZero() {
		q += " AND at>=?"
		args = append(args, f.Since.Unix())
	}
	q += " ORDER BY at DESC, id DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEvent
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit deletes events older than cutoff. Used by retention sweeps.
func (s *Store) PruneAudit(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE at < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanAudit(r rowScanner) (*AuditEvent, error) {
	var e AuditEvent
	var at int64
	if err := r.Scan(&e.ID, &at, &e.ActorType, &e.ActorID, &e.ActorLabel, &e.Action,
		&e.TargetType, &e.TargetID, &e.TargetName, &e.Outcome, &e.IP, &e.UserAgent, &e.Detail); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.At = time.Unix(at, 0)
	return &e, nil
}
