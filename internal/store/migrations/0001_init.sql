CREATE TABLE IF NOT EXISTS apps (
    id            TEXT PRIMARY KEY,
    name          TEXT UNIQUE NOT NULL,
    source_type   TEXT NOT NULL,           -- 'git' | 'drop'
    source_ref    TEXT NOT NULL,           -- git URL or drop UUID
    git_ref       TEXT NOT NULL DEFAULT '',
    drop_kind     TEXT NOT NULL DEFAULT '',-- 'static' | 'dockerfile' | ''
    compose_yaml  TEXT NOT NULL,
    env_json      TEXT NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS app_domains (
    app_id  TEXT NOT NULL,
    domain  TEXT NOT NULL,
    PRIMARY KEY (app_id, domain),
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_app_domains_domain ON app_domains(domain);

CREATE TABLE IF NOT EXISTS git_credentials (
    id          TEXT PRIMARY KEY,
    host        TEXT UNIQUE NOT NULL,      -- e.g. 'github.com'
    username    TEXT NOT NULL,
    token       TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    expires_at  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);