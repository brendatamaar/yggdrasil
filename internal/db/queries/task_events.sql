-- name: AppendTaskEvent :exec
INSERT INTO task_events (task_id, seq, kind, at, data) VALUES (?, ?, ?, ?, ?);

-- name: ListTaskEvents :many
SELECT * FROM task_events WHERE task_id = ? AND seq > ? ORDER BY seq;
