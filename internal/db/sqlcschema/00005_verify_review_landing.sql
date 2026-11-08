-- Phase 4 foundations (docs/design/verify.md, changesets.md, landing.md).
-- This file is the additive part of migration 00005 (internal/db/migrate00005.go
-- embeds and runs it) AND what sqlc reads, because 00005 is a Go migration:
-- the CHECK rebuilds of proposals and worktrees are in the Go file, and sqlc
-- does not need CHECKs. Keep every statement here
-- additive (CREATE TABLE, ADD COLUMN, CREATE INDEX): the Go migration runs it
-- once, in a transaction.

-- One verification per (task, repo): the state of core/verify.Verification.
-- state is a code, the other columns carry the variant's data; the shell
-- parses the row with StateFromRow and rejects an inconsistent one.
CREATE TABLE verifications (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    repo_id     TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    worktree_id TEXT NOT NULL REFERENCES worktrees (id) ON DELETE RESTRICT,
    version     INTEGER NOT NULL DEFAULT 0,
    -- The dependency hash the last successful bootstrap saw; '' = never.
    boot_hash   TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'none' CHECK (state IN
                    ('none', 'skipped', 'unverifiable', 'bootstrapping', 'verifying',
                     'passed', 'failed', 'interrupted', 'cancelled')),
    turn_id     TEXT NOT NULL DEFAULT '',
    attempt     INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    -- skip reason, unverifiable reason or cancel cause, by state.
    reason      TEXT NOT NULL DEFAULT '',
    -- failed: 'bootstrap' or 'verify'; interrupted: the stage that was lost.
    stage       TEXT NOT NULL DEFAULT '' CHECK (stage IN ('', 'bootstrap', 'verify')),
    -- The live build (bootstrapping, verifying) or the last build (passed,
    -- failed); NextBuild is the verify build chosen up front.
    build_id    TEXT NOT NULL DEFAULT '',
    next_build_id TEXT NOT NULL DEFAULT '',
    deps_hash   TEXT NOT NULL DEFAULT '',
    -- Failed: the failing step index and how it failed.
    fail_step   INTEGER NOT NULL DEFAULT 0 CHECK (fail_step >= 0),
    fail_kind   TEXT NOT NULL DEFAULT '' CHECK (fail_kind IN ('', 'exit_code', 'start_failed')),
    exit_code   INTEGER NOT NULL DEFAULT 0,
    fail_text   TEXT NOT NULL DEFAULT '',
    -- The Plan kept while a build is live, as JSON ('{}' otherwise).
    plan        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(plan)),
    since       TEXT,
    at          TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (task_id, repo_id)
) STRICT;

CREATE INDEX verifications_state ON verifications (state);
CREATE INDEX verifications_build ON verifications (build_id);

-- Review notes (RV-01): core/review.Note.
CREATE TABLE review_notes (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    repo_id    TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    line       INTEGER NOT NULL CHECK (line >= 1),
    side       TEXT NOT NULL CHECK (side IN ('old', 'new')),
    text       TEXT NOT NULL,
    diff_hash  TEXT NOT NULL CHECK (length(diff_hash) = 64),
    state      TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'sending', 'sent', 'outdated')),
    -- sending: the batch; sent: the turn that received it.
    batch_id   TEXT NOT NULL DEFAULT '',
    turn_id    TEXT NOT NULL DEFAULT '',
    state_at   TEXT,
    version    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX review_notes_task ON review_notes (task_id, repo_id);
CREATE INDEX review_notes_state ON review_notes (state);

-- Per-repo build settings (BL-08, WE-05).
CREATE TABLE repo_build_settings (
    repo_id      TEXT PRIMARY KEY REFERENCES repos (id) ON DELETE CASCADE,
    -- The verify chain override as JSON; NULL = use the toolchain default.
    verify       TEXT CHECK (verify IS NULL OR json_valid(verify)),
    -- Append the full-test chain to the verify chain.
    full_tests   INTEGER NOT NULL DEFAULT 0 CHECK (full_tests IN (0, 1)),
    -- Bootstrap a task worktree automatically before its first verification.
    bootstrap    INTEGER NOT NULL DEFAULT 1 CHECK (bootstrap IN (0, 1)),
    version      INTEGER NOT NULL DEFAULT 0,
    updated_at   TEXT NOT NULL
) STRICT;

-- Change sets (docs/design/changesets.md): how the last confirm run ended,
-- and a version for expected-version updates.
ALTER TABLE change_sets ADD COLUMN run_result TEXT NOT NULL DEFAULT '{"kind":"idle"}'
    CHECK (json_valid(run_result));
ALTER TABLE change_sets ADD COLUMN version INTEGER NOT NULL DEFAULT 0;

-- Worktree removal (docs/design/landing.md): the leaf of the staged folder
-- once the worktree was moved into <root>/.removing/; '' before that.
ALTER TABLE worktrees ADD COLUMN staged_name TEXT NOT NULL DEFAULT '';

-- Extra environment of a build step as a JSON array of KEY=VALUE strings.
ALTER TABLE build_steps ADD COLUMN env TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(env));
