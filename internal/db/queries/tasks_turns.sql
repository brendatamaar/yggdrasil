-- name: InsertTurn :exec
INSERT INTO task_turns (id, task_id, seq, mode, prompt, attempt, state, started_at, finished_at, outcome,
                        input_tokens, output_tokens, cache_write_tokens, cache_read_tokens,
                        cost_micro_usd, cost_known, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The prompt, sequence number and mode of a turn never change.
-- name: UpdateTurn :execrows
UPDATE task_turns
SET attempt = ?, state = ?, started_at = ?, finished_at = ?, outcome = ?,
    input_tokens = ?, output_tokens = ?, cache_write_tokens = ?, cache_read_tokens = ?,
    cost_micro_usd = ?, cost_known = ?, duration_ms = ?
WHERE id = ? AND task_id = ?;

-- name: GetTurn :one
SELECT * FROM task_turns WHERE id = ?;

-- name: GetLastTurn :one
SELECT * FROM task_turns WHERE task_id = ? ORDER BY seq DESC LIMIT 1;

-- name: GetTurnPrompt :one
SELECT prompt FROM task_turns WHERE id = ?;

-- name: ListTurnsByTask :many
SELECT * FROM task_turns WHERE task_id = ? ORDER BY seq;

-- name: UpdateTaskState :execrows
UPDATE tasks
SET status = ?, current_turn = ?, idle_outcome = ?, session_id = ?, finished_at = ?, updated_at = ?,
    version = version + 1
WHERE id = ? AND version = ?;

-- Tasks with a turn waiting or in flight, oldest first.
-- name: ListLiveTasks :many
SELECT * FROM tasks WHERE status IN ('queued', 'running') ORDER BY created_at, id;

-- Rows written before turns existed: queued or running with no turn.
-- name: ListLegacyLiveTasks :many
SELECT id, version FROM tasks
WHERE status IN ('queued', 'running') AND current_turn IS NULL ORDER BY id;

-- name: MarkLegacyInterrupted :execrows
UPDATE tasks
SET status = 'failed', idle_outcome = 'failed_interrupted', finished_at = ?, updated_at = ?,
    version = version + 1
WHERE id = ? AND version = ? AND status IN ('queued', 'running') AND current_turn IS NULL;

-- name: MaxTaskEventSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM task_events WHERE task_id = ?;

-- name: SetRepoTrust :execrows
UPDATE repos SET trusted = ?, trust_hash = ? WHERE id = ?;

-- name: GetRepoTrust :one
SELECT trusted, trust_hash FROM repos WHERE id = ?;
