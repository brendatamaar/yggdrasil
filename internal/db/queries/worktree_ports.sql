-- name: ListLiveWorktreePorts :many
SELECT * FROM worktree_ports WHERE released_at IS NULL ORDER BY base;

-- name: GetLiveWorktreePort :one
SELECT * FROM worktree_ports WHERE worktree_id = ? AND released_at IS NULL;

-- name: InsertWorktreePort :exec
INSERT INTO worktree_ports (worktree_id, base, size, allocated_at) VALUES (?, ?, ?, ?);

-- name: ReleaseWorktreePort :execrows
UPDATE worktree_ports SET released_at = ? WHERE worktree_id = ? AND released_at IS NULL;
