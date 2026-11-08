-- +goose Up
-- Phase 5 foundations (docs/design/devservers.md, worktree-env.md):
--  * dev_servers: one row per server (DV-01..DV-03). The id is built by the
--    shell from repo, worktree and command key. Logs are files, not rows;
--  * worktree_ports: the port block of a worktree (WE-02). Released blocks
--    stay as history (released_at), nothing is deleted;
--  * repo_env_settings: bootstrap profile, agent-copy toggle and compose
--    opt-in per repo (WE-01, WE-03).
-- Terminal tokens and sessions have no table: tokens live in memory and
-- open/close go to the audit log (docs/design/terminal.md).
-- Schema only; the queries are in queries/dev_servers.sql, worktree_ports.sql
-- and repo_env.sql.

CREATE TABLE dev_servers (
    id          TEXT PRIMARY KEY,
    repo_id     TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    worktree_id TEXT REFERENCES worktrees (id) ON DELETE CASCADE,
    command_key TEXT NOT NULL CHECK (command_key <> ''),
    status      TEXT NOT NULL CHECK (status IN ('stopped', 'starting', 'running', 'stopping', 'failed', 'orphaned')),
    attempt     INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    pid         INTEGER,
    -- JSON arrays of port numbers (observed from the OS; printed by the server).
    ports       TEXT NOT NULL DEFAULT '[]',
    announced   TEXT NOT NULL DEFAULT '[]',
    block_base  INTEGER NOT NULL DEFAULT 0 CHECK (block_base >= 0),
    block_size  INTEGER NOT NULL DEFAULT 0 CHECK (block_size >= 0),
    -- How a server at rest last ended (status stopped) or why it failed.
    last_kind   TEXT NOT NULL DEFAULT 'never' CHECK (last_kind IN ('never', 'exited', 'user_stopped', 'interrupted')),
    fail_kind   TEXT CHECK (fail_kind IS NULL OR fail_kind IN ('start_failed', 'exited_early', 'crashed', 'stop_failed')),
    exit_code   INTEGER NOT NULL DEFAULT 0,
    stop_cause  TEXT CHECK (stop_cause IS NULL OR stop_cause IN ('user_stop', 'restart', 'app_shutdown')),
    kill_attempt INTEGER NOT NULL DEFAULT 0 CHECK (kill_attempt BETWEEN 0 AND 2),
    since       TEXT,
    updated_at  TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    -- A live server has an attempt; running and stopping have a pid.
    CHECK (status IN ('stopped', 'failed', 'orphaned') OR attempt >= 1),
    CHECK (status NOT IN ('running', 'stopping', 'orphaned') OR (pid IS NOT NULL AND pid > 0)),
    CHECK ((status = 'failed') = (fail_kind IS NOT NULL)),
    CHECK ((status = 'stopping') = (stop_cause IS NOT NULL))
) STRICT;

CREATE INDEX dev_servers_repo ON dev_servers (repo_id);
CREATE INDEX dev_servers_worktree ON dev_servers (worktree_id);
CREATE INDEX dev_servers_status ON dev_servers (status);

CREATE TABLE worktree_ports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    worktree_id  TEXT NOT NULL REFERENCES worktrees (id) ON DELETE CASCADE,
    base         INTEGER NOT NULL CHECK (base >= 1 AND base <= 65535),
    size         INTEGER NOT NULL CHECK (size >= 1 AND base + size - 1 <= 65535),
    allocated_at TEXT NOT NULL,
    released_at  TEXT
) STRICT;

-- One live block per worktree, and live blocks never share a base.
CREATE UNIQUE INDEX worktree_ports_live_worktree ON worktree_ports (worktree_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX worktree_ports_live_base ON worktree_ports (base) WHERE released_at IS NULL;

CREATE TABLE repo_env_settings (
    repo_id                TEXT PRIMARY KEY REFERENCES repos (id) ON DELETE CASCADE,
    -- JSON array of relative paths; validated by worktreeenv.ParseProfile.
    profile_paths          TEXT NOT NULL DEFAULT '[]',
    -- Also copy the profile for agent tasks (off by default, decision 3).
    agent_copy             INTEGER NOT NULL DEFAULT 0 CHECK (agent_copy IN (0, 1)),
    compose_enabled        INTEGER NOT NULL DEFAULT 0 CHECK (compose_enabled IN (0, 1)),
    compose_file           TEXT NOT NULL DEFAULT '',
    -- The file hash the user confirmed (docs/design/worktree-env.md, C1).
    compose_confirmed_hash TEXT NOT NULL DEFAULT '',
    updated_at             TEXT NOT NULL,
    CHECK (compose_enabled = 0 OR compose_file <> '')
) STRICT;

-- +goose Down
DROP TABLE repo_env_settings;
DROP INDEX worktree_ports_live_base;
DROP INDEX worktree_ports_live_worktree;
DROP TABLE worktree_ports;
DROP INDEX dev_servers_status;
DROP INDEX dev_servers_worktree;
DROP INDEX dev_servers_repo;
DROP TABLE dev_servers;
