-- name: CreateWorktree :exec
INSERT INTO worktrees (id, repo_id, task_id, name, path, branch, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorktree :one
SELECT * FROM worktrees WHERE id = ?;

-- name: ListWorktreesByTask :many
SELECT * FROM worktrees WHERE task_id = ? ORDER BY created_at, id;

-- name: UpdateWorktreeStatus :execrows
UPDATE worktrees SET status = ? WHERE id = ?;
