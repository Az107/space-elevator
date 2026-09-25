-- Durable update state for an app: logical storage mappings, immutable
-- deployment releases, and the operation that currently owns an app.
ALTER TABLE apps ADD COLUMN current_release_id TEXT;

CREATE TABLE app_storage (
    app_id        TEXT NOT NULL,
    logical_name  TEXT NOT NULL,
    storage_kind  TEXT NOT NULL CHECK (storage_kind IN ('volume', 'bind')),
    physical_ref  TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (app_id, logical_name),
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);

CREATE TABLE app_releases (
    id                  TEXT PRIMARY KEY,
    app_id              TEXT NOT NULL,
    previous_release_id TEXT,
    source_type         TEXT NOT NULL,
    source_ref          TEXT NOT NULL,
    git_ref             TEXT NOT NULL DEFAULT '',
    git_commit          TEXT NOT NULL DEFAULT '',
    source_path         TEXT NOT NULL DEFAULT '',
    compose_yaml        TEXT NOT NULL,
    kind                TEXT NOT NULL DEFAULT 'web',
    build_mode          TEXT NOT NULL DEFAULT 'compose',
    builder_image       TEXT NOT NULL DEFAULT '',
    build_command       TEXT NOT NULL DEFAULT '',
    run_command         TEXT NOT NULL DEFAULT '',
    listen_port         INTEGER NOT NULL DEFAULT 0,
    image_map_json      TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending',
    last_error          TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    completed_at        INTEGER,
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE,
    FOREIGN KEY (previous_release_id) REFERENCES app_releases(id) ON DELETE SET NULL
);

CREATE INDEX idx_app_releases_app_id_created
    ON app_releases(app_id, created_at DESC);

CREATE TABLE app_operations (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL,
    operation_type TEXT NOT NULL,
    status         TEXT NOT NULL,
    heartbeat_at   INTEGER,
    error          TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    completed_at   INTEGER,
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_app_operations_one_active
    ON app_operations(app_id)
    WHERE status IN ('preflighting', 'building', 'backing_up', 'cutting_over', 'verifying', 'rolling_back');

CREATE INDEX idx_app_operations_app_id_created
    ON app_operations(app_id, created_at DESC);
