-- name: CreateBuild :exec
INSERT INTO builds (id, worktree_id, task_id, status, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetBuild :one
SELECT * FROM builds WHERE id = ?;

-- name: ListBuildsByTask :many
SELECT * FROM builds WHERE task_id = ? ORDER BY created_at, id;

-- name: UpdateBuildStatus :execrows
UPDATE builds SET status = ?, finished_at = ? WHERE id = ?;

-- name: CreateBuildStep :exec
INSERT INTO build_steps (build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListBuildSteps :many
SELECT * FROM build_steps WHERE build_id = ? ORDER BY idx;

-- name: UpdateBuildStep :execrows
UPDATE build_steps SET status = ?, exit_code = ?, log_path = ?, started_at = ?, finished_at = ?
WHERE build_id = ? AND idx = ?;
