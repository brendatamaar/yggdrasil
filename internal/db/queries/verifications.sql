-- name: InsertVerification :exec
INSERT INTO verifications (id, task_id, repo_id, worktree_id, boot_hash, state, turn_id, attempt, reason, stage,
                           build_id, next_build_id, deps_hash, fail_step, fail_kind, exit_code, fail_text,
                           plan, since, at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetVerification :one
SELECT * FROM verifications WHERE id = ?;

-- name: GetVerificationByTaskRepo :one
SELECT * FROM verifications WHERE task_id = ? AND repo_id = ?;

-- name: GetVerificationByBuild :one
SELECT * FROM verifications WHERE build_id = ? AND state IN ('bootstrapping', 'verifying');

-- name: ListVerificationsByTask :many
SELECT * FROM verifications WHERE task_id = ? ORDER BY repo_id;

-- name: ListLiveVerifications :many
SELECT * FROM verifications WHERE state IN ('bootstrapping', 'verifying') ORDER BY updated_at, id;

-- name: ListInterruptedVerifications :many
SELECT * FROM verifications WHERE state = 'interrupted' ORDER BY updated_at, id;

-- name: UpdateVerification :execrows
UPDATE verifications SET
    worktree_id = ?, boot_hash = ?, state = ?, turn_id = ?, attempt = ?, reason = ?, stage = ?,
    build_id = ?, next_build_id = ?, deps_hash = ?, fail_step = ?, fail_kind = ?, exit_code = ?, fail_text = ?,
    plan = ?, since = ?, at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND version = ?;

-- The last turn of every task that is idle in review: the reconcile loop's
-- trigger for verification (VF-01).
-- name: ListReviewTurns :many
SELECT t.id AS task_id, tt.id AS turn_id, tt.seq AS turn_seq
FROM tasks t
JOIN task_turns tt ON tt.task_id = t.id
WHERE t.status = 'review'
  AND tt.seq = (SELECT MAX(seq) FROM task_turns WHERE task_id = t.id)
  AND tt.state = 'finished' AND tt.outcome = 'ready_for_review'
ORDER BY t.id;
