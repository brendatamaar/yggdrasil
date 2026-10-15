-- name: CreateProposal :exec
INSERT INTO proposals (id, change_set_id, task_id, repo_id, kind, status, summary, payload, created_at, decided_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProposal :one
SELECT * FROM proposals WHERE id = ?;

-- name: ListProposalsByStatus :many
SELECT * FROM proposals WHERE status = ? ORDER BY created_at, id;

-- name: ListProposalsByChangeSet :many
SELECT * FROM proposals WHERE change_set_id = ? ORDER BY created_at, id;

-- name: UpdateProposalStatus :execrows
UPDATE proposals SET status = ?, decided_at = ? WHERE id = ?;
