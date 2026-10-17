-- +goose Up
-- Phase 2 (docs/design/builds.md, "Persistence and delivery"):
--  * a version on tasks, proposals and builds, for expected-version updates;
--  * builds and build_steps rebuilt with the new columns and wider CHECKs
--    (SQLite cannot alter a CHECK constraint);
--  * build_artifacts and build_presets.
-- The rebuild never drops a parent while a child still points at it, so the
-- ON DELETE CASCADE of build_steps cannot fire.

ALTER TABLE tasks ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proposals ADD COLUMN version INTEGER NOT NULL DEFAULT 0;

CREATE TABLE builds_new (
    id          TEXT PRIMARY KEY,
    worktree_id TEXT REFERENCES worktrees (id) ON DELETE SET NULL,
    task_id     TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    status      TEXT NOT NULL CHECK (status IN
                    ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'interrupted')),
    -- created_at is when the build was queued.
    created_at  TEXT NOT NULL,
    finished_at TEXT,
    version     INTEGER NOT NULL DEFAULT 0,
    actor       TEXT NOT NULL DEFAULT 'user',
    started_at  TEXT,
    result_step INTEGER,
    preset_id   TEXT
) STRICT;

CREATE TABLE build_steps_new (
    build_id        TEXT    NOT NULL REFERENCES builds_new (id) ON DELETE CASCADE,
    idx             INTEGER NOT NULL,
    repo_id         TEXT    NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN
                        ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped', 'interrupted')),
    exit_code       INTEGER,
    log_path        TEXT    NOT NULL DEFAULT '',
    started_at      TEXT,
    finished_at     TEXT,
    -- The command line as a JSON array of strings.
    argv            TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(argv)),
    attempt         INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT CHECK (outcome IS NULL OR outcome IN
                        ('succeeded', 'failed', 'start_failed', 'cancelled', 'interrupted')),
    start_failure   TEXT,
    log_truncated   INTEGER NOT NULL DEFAULT 0 CHECK (log_truncated IN (0, 1)),
    PRIMARY KEY (build_id, idx)
) STRICT, WITHOUT ROWID;

INSERT INTO builds_new (id, worktree_id, task_id, status, created_at, finished_at)
SELECT id, worktree_id, task_id, status, created_at, finished_at FROM builds;

INSERT INTO build_steps_new (build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at)
SELECT build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at FROM build_steps;

-- Children first: the old build_steps would be cascaded away with builds.
DROP TABLE build_steps;
DROP TABLE builds;
ALTER TABLE builds_new RENAME TO builds;
ALTER TABLE build_steps_new RENAME TO build_steps;

CREATE TABLE build_artifacts (
    build_id TEXT    NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    repo_id  TEXT    NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path     TEXT    NOT NULL,
    size     INTEGER NOT NULL,
    PRIMARY KEY (build_id, path)
) STRICT, WITHOUT ROWID;

CREATE TABLE build_presets (
    id         TEXT PRIMARY KEY,
    project_id TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    -- JSON array of steps (repo, name, argv).
    steps      TEXT    NOT NULL CHECK (json_valid(steps)),
    created_at TEXT    NOT NULL,
    version    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (project_id, name)
) STRICT;

-- +goose Down
DROP TABLE build_presets;
DROP TABLE build_artifacts;

-- Back to the 00001 shape. The wider statuses do not exist there:
-- interrupted becomes failed and skipped becomes cancelled.
CREATE TABLE builds_old (
    id          TEXT PRIMARY KEY,
    worktree_id TEXT REFERENCES worktrees (id) ON DELETE SET NULL,
    task_id     TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    status      TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    created_at  TEXT NOT NULL,
    finished_at TEXT
) STRICT;

CREATE TABLE build_steps_old (
    build_id    TEXT    NOT NULL REFERENCES builds_old (id) ON DELETE CASCADE,
    idx         INTEGER NOT NULL,
    repo_id     TEXT    NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    status      TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    exit_code   INTEGER,
    log_path    TEXT    NOT NULL DEFAULT '',
    started_at  TEXT,
    finished_at TEXT,
    PRIMARY KEY (build_id, idx)
) STRICT, WITHOUT ROWID;

INSERT INTO builds_old (id, worktree_id, task_id, status, created_at, finished_at)
SELECT id, worktree_id, task_id,
       CASE status WHEN 'interrupted' THEN 'failed' ELSE status END,
       created_at, finished_at
FROM builds;

INSERT INTO build_steps_old (build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at)
SELECT build_id, idx, repo_id, name,
       CASE status WHEN 'interrupted' THEN 'failed' WHEN 'skipped' THEN 'cancelled' ELSE status END,
       exit_code, log_path, started_at, finished_at
FROM build_steps;

DROP TABLE build_steps;
DROP TABLE builds;
ALTER TABLE builds_old RENAME TO builds;
ALTER TABLE build_steps_old RENAME TO build_steps;

ALTER TABLE proposals DROP COLUMN version;
ALTER TABLE tasks DROP COLUMN version;
