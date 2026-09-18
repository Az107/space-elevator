-- Append-only audit trail for security-relevant actions: logins,
-- account/token changes, deploys, removals, and config edits. Rows are
-- written by the web/API handlers, the deployer pipeline, and the CLI.
-- No secret or password values are ever stored here; detail carries
-- only human-readable context.
CREATE TABLE IF NOT EXISTS audit_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    actor_type  TEXT NOT NULL,               -- 'user' | 'token' | 'cli' | 'system' | 'anonymous'
    actor_id    TEXT NOT NULL DEFAULT '',
    actor_label TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,               -- e.g. 'auth.login', 'app.deploy'
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    target_name TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL,               -- 'success' | 'failure'
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_events_at ON audit_events(at);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events(action);
CREATE INDEX IF NOT EXISTS idx_audit_events_target ON audit_events(target_name);
