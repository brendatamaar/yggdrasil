-- +goose NO TRANSACTION
-- +goose Up
-- Phase 2 (docs/design/repo-tracking.md, "Persistence and delivery"):
--  * soft untrack: projects.untracked_at and repos.untracked_at;
--  * projects.version, for expected-version updates of a decision;
--  * repos.rel_path, the repo's folder under its project, unique per project;
--  * the project name is unique among ACTIVE projects only (partial index),
--    which needs a rebuild of projects: SQLite cannot drop a column UNIQUE;
--  * settings, a small key/value table for user settings such as the editor.
--
-- This migration runs outside goose's transaction on purpose. The rebuild
-- drops projects while repos, tasks and build_presets point at it with
-- ON DELETE CASCADE, and foreign_keys cannot be switched off inside a
-- transaction. The write pool has one connection, so the pragma and the
-- explicit transaction below share it (SQLite's documented 12-step ALTER).
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE projects_new (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    path         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    -- Non-null: untracked (hidden everywhere, rows and history kept).
    untracked_at TEXT,
    version      INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO projects_new (id, name, path, created_at)
SELECT id, name, path, created_at FROM projects;

DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

CREATE UNIQUE INDEX projects_active_name ON projects (name) WHERE untracked_at IS NULL;

ALTER TABLE repos ADD COLUMN untracked_at TEXT;
-- The folder under projects.path with forward slashes, "." for the project
-- folder itself. Existing rows: derived from repos.path, falling back to the
-- repo name (unique per project) when the repo lies outside the project path.
ALTER TABLE repos ADD COLUMN rel_path TEXT NOT NULL DEFAULT '.';

UPDATE repos SET rel_path = COALESCE((
    SELECT CASE
        WHEN lower(rtrim(replace(repos.path, '\', '/'), '/')) = lower(rtrim(replace(p.path, '\', '/'), '/')) THEN '.'
        WHEN lower(substr(rtrim(replace(repos.path, '\', '/'), '/'), 1, length(rtrim(replace(p.path, '\', '/'), '/')) + 1))
             = lower(rtrim(replace(p.path, '\', '/'), '/')) || '/'
            THEN substr(rtrim(replace(repos.path, '\', '/'), '/'), length(rtrim(replace(p.path, '\', '/'), '/')) + 2)
        ELSE repos.name
    END
    FROM projects p WHERE p.id = repos.project_id
), repos.name);

CREATE UNIQUE INDEX repos_project_rel_path ON repos (project_id, rel_path);

CREATE TABLE settings (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0
) STRICT;

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

DROP TABLE settings;
DROP INDEX repos_project_rel_path;
ALTER TABLE repos DROP COLUMN rel_path;
ALTER TABLE repos DROP COLUMN untracked_at;

-- 00001 wants one name per project. Untracked projects may share a name with
-- an active one, so make theirs unique; nothing is deleted.
UPDATE projects SET name = name || ' (' || id || ')' WHERE untracked_at IS NOT NULL;

CREATE TABLE projects_old (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    path       TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

INSERT INTO projects_old (id, name, path, created_at)
SELECT id, name, path, created_at FROM projects;

DROP TABLE projects;
ALTER TABLE projects_old RENAME TO projects;

COMMIT;
PRAGMA foreign_keys = ON;
