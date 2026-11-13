-- Phase 6 queries (docs/design/phase-6-*.md). Schema: sqlcschema/00007_phase6.sql.

-- Repo reservations (NR-01).

-- name: InsertReservation :exec
INSERT INTO repo_reservations (repo_id, operation_id, project_id, leaf, setup_task_id, proposal_id,
                               spec, spec_hash, state, stage, attempt, detail, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetReservation :one
SELECT * FROM repo_reservations WHERE operation_id = ?;

-- name: GetReservationByRepo :one
SELECT * FROM repo_reservations WHERE repo_id = ?;

-- Operations a restart must recover: a stage may have been running.
-- name: ListUnsettledReservations :many
SELECT * FROM repo_reservations WHERE state IN ('initializing', 'publishing', 'uncertain') ORDER BY created_at, operation_id;

-- name: ListReservationsByProject :many
SELECT * FROM repo_reservations WHERE project_id = ? ORDER BY created_at, operation_id;

-- name: UpdateReservation :execrows
UPDATE repo_reservations
SET proposal_id = ?, state = ?, stage = ?, attempt = ?, detail = ?, updated_at = ?, version = version + 1
WHERE operation_id = ? AND version = ?;

-- Proposal effects: the durable record of an effect outside the database.

-- name: InsertEffect :exec
INSERT INTO proposal_effects (id, proposal_id, operation_id, stage, attempt, state, receipt_kind, receipt,
                              marker, remote_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEffect :one
SELECT * FROM proposal_effects WHERE id = ?;

-- name: GetEffectByKey :one
SELECT * FROM proposal_effects WHERE proposal_id = ? AND stage = ? AND attempt = ?;

-- name: ListEffectsByProposal :many
SELECT * FROM proposal_effects WHERE proposal_id = ? ORDER BY created_at, id;

-- name: ListEffectsByOperation :many
SELECT * FROM proposal_effects WHERE operation_id = ? ORDER BY created_at, id;

-- Effects that were started and never settled: a restart reconciles them.
-- name: ListOpenEffects :many
SELECT * FROM proposal_effects WHERE state IN ('intent', 'unknown') ORDER BY created_at, id;

-- name: SettleEffect :execrows
UPDATE proposal_effects
SET state = ?, receipt_kind = ?, receipt = ?, remote_id = ?, updated_at = ?, version = version + 1
WHERE id = ? AND version = ?;

-- Workspace action requests (AC-01 to AC-06).

-- name: InsertActionRequest :exec
INSERT INTO workspace_action_requests (request_id, digest, repo_id, kind, source_id, state, attempt,
                                       idempotency_key, refusal, task_id, batch_id, created_at, updated_at, agent, model)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetActionRequest :one
SELECT * FROM workspace_action_requests WHERE request_id = ?;

-- Requests a restart must reconcile (an intent was persisted, no result).
-- name: ListUnsettledActionRequests :many
SELECT * FROM workspace_action_requests WHERE state = 'submitting' ORDER BY created_at, request_id;

-- name: UpdateActionRequest :execrows
UPDATE workspace_action_requests
SET state = ?, attempt = ?, idempotency_key = ?, refusal = ?, task_id = ?, batch_id = ?,
    updated_at = ?, version = version + 1
WHERE request_id = ? AND version = ?;

-- name: AddActionProposal :exec
INSERT INTO workspace_action_proposals (request_id, proposal_id) VALUES (?, ?)
ON CONFLICT (request_id, proposal_id) DO NOTHING;

-- name: ListActionProposals :many
SELECT proposal_id FROM workspace_action_proposals WHERE request_id = ? ORDER BY proposal_id;

-- Outbox.

-- name: InsertOutbox :execrows
INSERT INTO outbox (id, subject_kind, subject_id, attempt, stage, payload, due_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- Claim up to :limit due rows whose lease is free or expired. A row that was
-- claimed before comes back with claim_count > 1: the worker reconciles first.
-- name: ClaimOutbox :many
UPDATE outbox
SET claimed_by = sqlc.arg(worker), claimed_until = sqlc.arg(lease_until),
    claim_count = claim_count + 1, version = version + 1
WHERE id IN (
    SELECT o.id FROM outbox o
    WHERE o.done_at IS NULL AND o.due_at <= sqlc.arg(now)
      AND (o.claimed_until IS NULL OR o.claimed_until <= sqlc.arg(now))
    ORDER BY o.due_at, o.id LIMIT sqlc.arg(max_rows))
RETURNING *;

-- name: CompleteOutbox :execrows
UPDATE outbox SET done_at = ?, claimed_by = '', claimed_until = NULL, version = version + 1
WHERE id = ? AND claimed_by = ? AND version = sqlc.arg(claim_version) AND done_at IS NULL;

-- name: ReleaseOutbox :execrows
UPDATE outbox SET due_at = ?, claimed_by = '', claimed_until = NULL, version = version + 1
WHERE id = ? AND claimed_by = ? AND version = sqlc.arg(claim_version) AND done_at IS NULL;

-- name: GetOutbox :one
SELECT * FROM outbox WHERE id = ?;

-- Notices (UI-06).

-- name: InsertNotice :one
INSERT INTO notices (id, task_id, task_title, status, source_kind, source_run, source_revision, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING cursor;

-- name: GetNotice :one
SELECT * FROM notices WHERE id = ?;

-- Oldest first after a cursor, for reconnect gap recovery.
-- name: ListNoticesAfter :many
SELECT * FROM notices WHERE cursor > ? ORDER BY cursor LIMIT ?;

-- Newest first, for the first page of the history.
-- name: ListNoticesRecent :many
SELECT * FROM notices ORDER BY cursor DESC LIMIT ?;

-- name: MaxNoticeCursor :one
SELECT CAST(COALESCE(MAX(cursor), 0) AS INTEGER) FROM notices;

-- name: MinNoticeCursor :one
SELECT CAST(COALESCE(MIN(cursor), 0) AS INTEGER) FROM notices;

-- Keep the newest :keep notices. The high-water marks are separate, so this
-- never lets an old transition notify again.
-- name: TrimNotices :execrows
DELETE FROM notices
WHERE cursor <= COALESCE((SELECT n.cursor FROM notices n ORDER BY n.cursor DESC LIMIT 1 OFFSET ?), 0);

-- name: UpsertNoticeHighWater :execrows
INSERT INTO notice_high_water (task_id, source_kind, source_run, revision, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (task_id, source_kind, source_run) DO UPDATE
SET revision = excluded.revision, updated_at = excluded.updated_at
WHERE excluded.revision > notice_high_water.revision;

-- name: ListNoticeHighWater :many
SELECT * FROM notice_high_water ORDER BY task_id, source_kind, source_run;

-- name: GetTaskSubmission :one
SELECT * FROM task_submissions WHERE request_id = ?;

-- name: LinkTaskSubmission :exec
INSERT INTO task_submissions (request_id, digest, task_id) VALUES (?, ?, ?);

-- name: GetTrackedReadyRepo :one
SELECT r.* FROM repos r JOIN projects p ON p.id = r.project_id
WHERE r.id = ? AND r.project_id = ? AND r.untracked_at IS NULL
  AND r.readiness = 'ready' AND p.untracked_at IS NULL;
