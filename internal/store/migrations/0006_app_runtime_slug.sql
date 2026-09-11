-- Stable runtime identity for an app: container/network/image names,
-- the Traefik route file, and the space-elevator.app label all derive
-- from this value. It is set at deploy time (to the app name) and never
-- changes when the app is renamed, so a rename only affects the display
-- name and dashboard URL.
ALTER TABLE apps ADD COLUMN slug TEXT NOT NULL DEFAULT '';
UPDATE apps SET slug = name WHERE slug = '';
