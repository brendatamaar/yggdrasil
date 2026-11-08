-- name: GetDevServer :one
SELECT * FROM dev_servers WHERE id = ?;

-- name: ListDevServersByRepo :many
SELECT * FROM dev_servers WHERE repo_id = ? ORDER BY id;

-- name: ListLiveDevServers :many
SELECT * FROM dev_servers WHERE status IN ('starting', 'running', 'stopping', 'orphaned') ORDER BY id;

-- name: InsertDevServer :exec
INSERT INTO dev_servers (id, repo_id, worktree_id, command_key, status, attempt, pid, ports, announced,
                         block_base, block_size, last_kind, fail_kind, exit_code, stop_cause, kill_attempt,
                         since, updated_at, version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0);

-- name: UpdateDevServer :execrows
UPDATE dev_servers
SET status = ?, attempt = ?, pid = ?, ports = ?, announced = ?, block_base = ?, block_size = ?,
    last_kind = ?, fail_kind = ?, exit_code = ?, stop_cause = ?, kill_attempt = ?, since = ?,
    updated_at = ?, version = version + 1
WHERE id = ? AND version = ?;
