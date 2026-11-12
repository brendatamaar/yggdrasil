-- Phase 6 foundations (docs/design/phase-6-*.md). This file is the additive
-- part of migration 00007 (internal/db/migrate00007.go embeds and runs it) AND
-- what sqlc reads, because 00007 is a Go migration: the CHECK rebuild of
-- proposals (new kinds) is in the Go file, and sqlc does not need CHECKs on it.
-- Keep every statement here additive (CREATE TABLE, ADD COLUMN, CREATE INDEX):
-- the Go migration runs it once, in a transaction.

-- Repo readiness (NR-01). A reserved repo is only named: no directory exists
-- and every ordinary query filters it out in SQL. Existing rows are ready.
ALTER TABLE repos ADD COLUMN readiness TEXT NOT NULL DEFAULT 'ready'
    CHECK (readiness IN ('reserved', 'ready'));
CREATE INDEX repos_project_readiness ON repos (project_id, readiness);

-- Task provenance (AC-05, NR-01). source_kind '' is an ordinary task; 'issue'
-- carries the host issue identity and the dedup key; 'repo_setup' marks the
-- internal draft that owns a new repo's initialization proposal.
ALTER TABLE tasks ADD COLUMN source_kind TEXT NOT NULL DEFAULT ''
    CHECK (source_kind IN ('', 'issue', 'repo_setup'));
ALTER TABLE tasks ADD COLUMN source_instance TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN source_project TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN source_number INTEGER NOT NULL DEFAULT 0 CHECK (source_number >= 0);
ALTER TABLE tasks ADD COLUMN dedup_key TEXT NOT NULL DEFAULT '';
-- One draft per issue per project.
CREATE UNIQUE INDEX tasks_project_dedup ON tasks (project_id, dedup_key) WHERE dedup_key <> '';
CREATE INDEX tasks_source_kind ON tasks (source_kind) WHERE source_kind <> '';

-- No agent ever runs in a repo-setup draft (NR-01): it stays a draft for good
-- and gets no turn. The core refuses it too (core.Task.Validate); these
-- triggers keep a bug or a direct write from starting one.
CREATE TRIGGER tasks_setup_stays_draft
BEFORE UPDATE OF status ON tasks
WHEN OLD.source_kind = 'repo_setup' AND NEW.status <> 'draft'
BEGIN
    SELECT RAISE(ABORT, 'a repo-setup draft cannot start');
END;

CREATE TRIGGER task_turns_not_for_setup_draft
BEFORE INSERT ON task_turns
WHEN (SELECT source_kind FROM tasks WHERE id = NEW.task_id) = 'repo_setup'
BEGIN
    SELECT RAISE(ABORT, 'a repo-setup draft cannot start');
END;

-- A new-repo operation (docs/design/phase-6-newrepos.md): the reservation of a
-- repo identity, its setup draft task and initialization proposal, and the
-- sealed core/newrepos state as codes plus JSON (the shell parses the row with
-- the core parsers and rejects an inconsistent one). One row per reserved repo;
-- the leaf is unique per project.
CREATE TABLE repo_reservations (
    repo_id       TEXT PRIMARY KEY REFERENCES repos (id) ON DELETE CASCADE,
    operation_id  TEXT NOT NULL UNIQUE,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    leaf          TEXT NOT NULL CHECK (leaf <> ''),
    setup_task_id TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    proposal_id   TEXT REFERENCES proposals (id) ON DELETE SET NULL,
    -- The immutable InitSpec as JSON and its hash.
    spec          TEXT NOT NULL CHECK (json_valid(spec)),
    spec_hash     TEXT NOT NULL CHECK (length(spec_hash) = 64),
    state         TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN
                      ('reserved', 'awaiting_confirm', 'initializing', 'ready',
                       'publishing', 'published', 'uncertain', 'stopped')),
    -- The stage of initializing, publishing, uncertain and stopped; '' otherwise.
    stage         TEXT NOT NULL DEFAULT '',
    attempt       INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    -- Variant data of the state (publish spec, stop reason, resume data); '{}'
    -- when the state has none. Never holds a token or a credential URL.
    detail        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(detail)),
    version       INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE (project_id, leaf)
) STRICT;

CREATE INDEX repo_reservations_state ON repo_reservations (state);
CREATE INDEX repo_reservations_setup_task ON repo_reservations (setup_task_id);

-- Durable effect records of proposals that act outside the database: a review
-- comment post, and the stages of new-repo initialization and publish. One row
-- per (proposal, stage, attempt), written as 'intent' BEFORE the effect and
-- settled after. receipt holds the sealed receipt JSON (never a token).
CREATE TABLE proposal_effects (
    id           TEXT PRIMARY KEY,
    proposal_id  TEXT NOT NULL REFERENCES proposals (id) ON DELETE CASCADE,
    -- The new-repo operation, '' for other kinds.
    operation_id TEXT NOT NULL DEFAULT '',
    -- '' for a single-effect kind (review comment).
    stage        TEXT NOT NULL DEFAULT '',
    attempt      INTEGER NOT NULL CHECK (attempt >= 1),
    state        TEXT NOT NULL DEFAULT 'intent' CHECK (state IN ('intent', 'succeeded', 'failed', 'unknown')),
    receipt_kind TEXT NOT NULL DEFAULT '',
    receipt      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(receipt)),
    -- The operation marker written before a remote effect, so a lost reply can
    -- be found again; '' when the kind has none.
    marker       TEXT NOT NULL DEFAULT '',
    -- The host's ID of the created object (comment ID, remote repository ID).
    remote_id    TEXT NOT NULL DEFAULT '',
    version      INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (proposal_id, stage, attempt),
    -- A settled effect has a receipt kind.
    CHECK (state NOT IN ('succeeded') OR receipt_kind <> '')
) STRICT;

CREATE INDEX proposal_effects_operation ON proposal_effects (operation_id) WHERE operation_id <> '';
CREATE INDEX proposal_effects_state ON proposal_effects (state);

-- One idempotent submission of a workspace action (AC-01 to AC-06): the
-- RequestID the browser generated, a digest of the request (a reused key with
-- another digest is refused), the tracked repo, the state of the dispatch and
-- the links to what it produced.
CREATE TABLE workspace_action_requests (
    request_id      TEXT PRIMARY KEY,
    digest          TEXT NOT NULL CHECK (length(digest) = 64),
    repo_id         TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN
                        ('diagnose_pipeline', 'fix_pipeline', 'review_mr', 'propose_commits',
                         'pull_and_check', 'import_issue', 'diagnose_build', 'diagnose_repo',
                         'propose_ignore')),
    source_id       TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL CHECK (state IN
                        ('preparing', 'ready', 'submitting', 'completed', 'refused', 'cancelled')),
    attempt         INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    idempotency_key TEXT NOT NULL DEFAULT '',
    refusal         TEXT NOT NULL DEFAULT '',
    -- The ordinary task or draft the request produced.
    task_id         TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    batch_id        TEXT NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    -- A completed request has what it produced; a refused one has a reason.
    CHECK (state <> 'refused' OR refusal <> ''),
    CHECK (state <> 'submitting' OR idempotency_key <> '')
) STRICT;

CREATE INDEX workspace_action_requests_repo ON workspace_action_requests (repo_id);
CREATE INDEX workspace_action_requests_state ON workspace_action_requests (state);

-- The proposals a request produced.
CREATE TABLE workspace_action_proposals (
    request_id  TEXT NOT NULL REFERENCES workspace_action_requests (request_id) ON DELETE CASCADE,
    proposal_id TEXT NOT NULL REFERENCES proposals (id) ON DELETE CASCADE,
    PRIMARY KEY (request_id, proposal_id)
) STRICT, WITHOUT ROWID;

-- A generic outbox for durable effects (docs/design: distributed profile for
-- action dispatch and the new-repo stages). The ID is deterministic
-- ("<subject_id>#<attempt>#<stage>"), so re-inserting the same intent is a
-- no-op. A worker claims a due row with a lease; a row whose lease expired is
-- claimed again with claim_count > 0, which tells the worker to RECONCILE
-- before doing anything (never an unconditional resend).
CREATE TABLE outbox (
    id            TEXT PRIMARY KEY,
    subject_kind  TEXT NOT NULL CHECK (subject_kind IN ('action_request', 'proposal_effect')),
    subject_id    TEXT NOT NULL CHECK (subject_id <> ''),
    attempt       INTEGER NOT NULL CHECK (attempt >= 1),
    stage         TEXT NOT NULL DEFAULT '',
    payload       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    due_at        TEXT NOT NULL,
    claimed_by    TEXT NOT NULL DEFAULT '',
    claimed_until TEXT,
    claim_count   INTEGER NOT NULL DEFAULT 0 CHECK (claim_count >= 0),
    done_at       TEXT,
    version       INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    created_at    TEXT NOT NULL,
    UNIQUE (subject_kind, subject_id, attempt, stage)
) STRICT;

CREATE INDEX outbox_due ON outbox (due_at) WHERE done_at IS NULL;

-- Notification history (UI-06, docs/design/phase-6-notifications.md). cursor
-- orders history and pages it; the unique source revision stops a replayed
-- transition from creating a second notice. Only the title and status are
-- stored: there is no column for a prompt, path, diff or log.
CREATE TABLE notices (
    cursor          INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT NOT NULL UNIQUE,
    task_id         TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    task_title      TEXT NOT NULL CHECK (task_title <> ''),
    status          TEXT NOT NULL CHECK (status IN
                        ('needs_you', 'verification_passed', 'verification_failed',
                         'ready_for_review', 'done', 'failed', 'cancelled')),
    source_kind     TEXT NOT NULL CHECK (source_kind IN ('task', 'verification')),
    source_run      TEXT NOT NULL DEFAULT '',
    source_revision INTEGER NOT NULL CHECK (source_revision >= 1),
    created_at      TEXT NOT NULL,
    UNIQUE (task_id, source_kind, source_run, source_revision),
    CHECK ((source_kind = 'verification') = (source_run <> ''))
) STRICT;

-- The producer's durable high-water mark per (task, source): separate from the
-- retained notices, so trimming history never re-enables an old notice.
CREATE TABLE notice_high_water (
    task_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('task', 'verification')),
    source_run  TEXT NOT NULL DEFAULT '',
    revision    INTEGER NOT NULL CHECK (revision >= 1),
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (task_id, source_kind, source_run),
    CHECK ((source_kind = 'verification') = (source_run <> ''))
) STRICT, WITHOUT ROWID;
