CREATE TABLE IF NOT EXISTS app_secrets (
    app_id TEXT NOT NULL,
    key    TEXT NOT NULL,
    value  TEXT NOT NULL,
    PRIMARY KEY (app_id, key),
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);
