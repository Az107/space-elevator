-- Session hardening:
--   * session IDs are now stored as SHA-256 hashes of the cookie token, so
--     existing plaintext rows must be invalidated (one forced re-login).
--   * sliding idle timeout needs a last_seen_at column.
-- Deploy error surfacing needs a place to store the failure reason.
DELETE FROM sessions;
ALTER TABLE sessions ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
