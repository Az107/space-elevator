-- First-class static web builds: build the site, then serve the selected
-- repository-relative directory with the platform's Nginx image/config.
ALTER TABLE apps ADD COLUMN serve_path TEXT NOT NULL DEFAULT '';
ALTER TABLE app_releases ADD COLUMN serve_path TEXT NOT NULL DEFAULT '';
