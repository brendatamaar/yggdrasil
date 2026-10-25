-- +goose Up
-- Phase 3 foundations (docs/design/tasks.md, guard.md, proposals.md):
--  * task_turns: a task has turns, each with its own mode, attempt and usage
--    (AG-10, AG-15); tasks.current_turn and tasks.idle_outcome carry the state
--    that Status alone cannot (core/tasks StateFromRows);
--  * repos.trust_hash: the hash of the agent config the user trusted (SF-09);
--  * worktrees.base_commit: the commit a task worktree started from;
--  * proposals: the action hash the user confirms, the execution attempt, who
--    created it, and how it ended (RV-02).
-- Schema only; the queries come with the services that use them.

CREATE TABLE task_turns (
    id          TEXT PRIMARY KEY,
    task_id     TEXT    NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL CHECK (seq >= 1),
    mode        TEXT    NOT NULL CHECK (mode IN ('ask', 'code', 'current_branch', 'project')),
    -- Stored, never audited or notified.
    prompt      TEXT    NOT NULL,
    attempt     INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    state       TEXT    NOT NULL CHECK (state IN ('waiting', 'starting', 'running', 'stopping', 'finished')),
    started_at  TEXT,
    finished_at TEXT,
    -- How the turn ended; the same codes as tasks.idle_outcome.
    outcome     TEXT CHECK (outcome IS NULL OR outcome IN
                    ('answered', 'ready_for_review', 'needs_integrity_violated', 'needs_cap_exceeded',
                     'failed_prepare_failed', 'failed_start_failed', 'failed_run_failed',
                     'failed_interrupted', 'cancelled', 'dismissed')),
    input_tokens       INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens      INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    -- Micro-USD, so sums are exact. cost_known is 0 when the adapter reports
    -- no cost: zero then means "unknown", not "free".
    cost_micro_usd     INTEGER NOT NULL DEFAULT 0 CHECK (cost_micro_usd >= 0),
    cost_known         INTEGER NOT NULL DEFAULT 0 CHECK (cost_known IN (0, 1)),
    duration_ms        INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    UNIQUE (task_id, seq),
    -- A turn is finished exactly when it has a finish time and an outcome.
    CHECK ((state = 'finished') = (finished_at IS NOT NULL)),
    CHECK ((state = 'finished') = (outcome IS NOT NULL)),
    -- A live turn has started.
    CHECK (state IN ('waiting', 'finished') OR started_at IS NOT NULL),
    -- Cost can only be positive when it is known.
    CHECK (cost_known = 1 OR cost_micro_usd = 0)
) STRICT;

-- The turn in flight (or the last one), and how the task went idle. Both are
-- NULL for a task that never had a turn (draft, and tasks from Phase 2).
ALTER TABLE tasks ADD COLUMN current_turn TEXT;
ALTER TABLE tasks ADD COLUMN idle_outcome TEXT CHECK (idle_outcome IS NULL OR idle_outcome IN
    ('answered', 'ready_for_review', 'needs_integrity_violated', 'needs_cap_exceeded',
     'failed_prepare_failed', 'failed_start_failed', 'failed_run_failed',
     'failed_interrupted', 'cancelled', 'dismissed'));

-- Empty when the repo is not trusted; else sha256 hex of its agent config.
ALTER TABLE repos ADD COLUMN trust_hash TEXT NOT NULL DEFAULT ''
    CHECK (trust_hash = '' OR length(trust_hash) = 64);

ALTER TABLE worktrees ADD COLUMN base_commit TEXT NOT NULL DEFAULT '';

ALTER TABLE proposals ADD COLUMN action_hash TEXT NOT NULL DEFAULT ''
    CHECK (action_hash = '' OR length(action_hash) = 64);
ALTER TABLE proposals ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0);
ALTER TABLE proposals ADD COLUMN source TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('agent', 'user'));
ALTER TABLE proposals ADD COLUMN fail_kind TEXT CHECK (fail_kind IS NULL OR fail_kind IN
    ('stale', 'precondition', 'repo_untracked', 'rejected_by_remote', 'unsafe_remote',
     'interrupted', 'exec_error'));
-- Plain facts about what an executed proposal produced; never tokens.
ALTER TABLE proposals ADD COLUMN result TEXT CHECK (result IS NULL OR json_valid(result));
ALTER TABLE proposals ADD COLUMN message TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1));

-- The pending count of a task, checked before a proposal is created.
CREATE INDEX proposals_task_status ON proposals (task_id, status);

-- +goose Down
DROP INDEX proposals_task_status;
ALTER TABLE proposals DROP COLUMN retryable;
ALTER TABLE proposals DROP COLUMN message;
ALTER TABLE proposals DROP COLUMN result;
ALTER TABLE proposals DROP COLUMN fail_kind;
ALTER TABLE proposals DROP COLUMN source;
ALTER TABLE proposals DROP COLUMN attempt;
ALTER TABLE proposals DROP COLUMN action_hash;
ALTER TABLE worktrees DROP COLUMN base_commit;
ALTER TABLE repos DROP COLUMN trust_hash;
ALTER TABLE tasks DROP COLUMN idle_outcome;
ALTER TABLE tasks DROP COLUMN current_turn;
DROP TABLE task_turns;
