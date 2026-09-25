-- Durable deployment progress. The operation row is the job; these tables
-- expose its plan and output without keeping the whole build feed in memory.
ALTER TABLE app_operations ADD COLUMN current_step TEXT NOT NULL DEFAULT '';
ALTER TABLE app_operations ADD COLUMN started_at INTEGER;
ALTER TABLE app_operations ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE app_operations ADD COLUMN candidate_release_id TEXT NOT NULL DEFAULT '';
ALTER TABLE app_operations ADD COLUMN previous_release_id TEXT NOT NULL DEFAULT '';

CREATE TABLE deploy_steps (
    operation_id TEXT NOT NULL,
    position     INTEGER NOT NULL,
    step_key     TEXT NOT NULL,
    label        TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    error        TEXT NOT NULL DEFAULT '',
    started_at   INTEGER,
    completed_at INTEGER,
    PRIMARY KEY (operation_id, step_key),
    UNIQUE (operation_id, position),
    FOREIGN KEY(operation_id) REFERENCES app_operations(id) ON DELETE CASCADE
);

CREATE TABLE deploy_logs (
    operation_id TEXT NOT NULL,
    sequence     INTEGER NOT NULL,
    step_key     TEXT NOT NULL DEFAULT '',
    level        TEXT NOT NULL DEFAULT 'info',
    message      TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (operation_id, sequence),
    FOREIGN KEY(operation_id) REFERENCES app_operations(id) ON DELETE CASCADE
);

CREATE INDEX idx_deploy_logs_operation_sequence
    ON deploy_logs(operation_id, sequence);
