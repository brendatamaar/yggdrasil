-- For sqlc only (it is not embedded and goose never reads it). Migration
-- 00003 is a Go migration (migrate00003.go); this file is its schema effect,
-- so the generated queries see the tables as they are after it. Keep both in
-- step: a test compares them (TestSqlcSchemaMatchesMigration).

CREATE TABLE projects_new (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    path         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    untracked_at TEXT,
    version      INTEGER NOT NULL DEFAULT 0
) STRICT;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
CREATE UNIQUE INDEX projects_active_name ON projects (name) WHERE untracked_at IS NULL;
ALTER TABLE repos ADD COLUMN untracked_at TEXT;
ALTER TABLE repos ADD COLUMN rel_path TEXT NOT NULL DEFAULT '.';
CREATE UNIQUE INDEX repos_project_rel_path ON repos (project_id, rel_path);
CREATE TABLE settings (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0
) STRICT;
