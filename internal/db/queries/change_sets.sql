-- name: CreateChangeSet :exec
INSERT INTO change_sets (id, task_id, status, merge_order, created_at) VALUES (?, ?, ?, ?, ?);

-- name: GetChangeSet :one
SELECT * FROM change_sets WHERE id = ?;

-- name: ListChangeSetsByTask :many
SELECT * FROM change_sets WHERE task_id = ? ORDER BY created_at, id;

-- name: UpdateChangeSetStatus :execrows
UPDATE change_sets SET status = ? WHERE id = ?;
