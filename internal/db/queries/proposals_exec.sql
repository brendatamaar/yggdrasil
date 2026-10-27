-- name: CreateProposalRecord :exec
INSERT INTO proposals (id, change_set_id, task_id, repo_id, kind, status, summary, payload, created_at, decided_at,
                       action_hash, attempt, source, fail_kind, result, message, retryable)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SaveProposalRecord :execrows
UPDATE proposals
SET status = ?, decided_at = ?, attempt = MAX(attempt, CAST(? AS INTEGER)), fail_kind = ?, result = ?,
    message = ?, retryable = ?, version = version + 1
WHERE id = ? AND version = ?;

-- name: CountPendingProposalsByTask :one
SELECT COUNT(*) FROM proposals WHERE task_id = ? AND status = 'pending';

-- name: ListProposalsByTask :many
SELECT * FROM proposals WHERE task_id = ? ORDER BY created_at, id;

-- name: GetFirstChangeSetByTask :one
SELECT * FROM change_sets WHERE task_id = ? ORDER BY created_at, id LIMIT 1;
