-- name: InsertReviewNote :exec
INSERT INTO review_notes (id, task_id, repo_id, path, line, side, text, diff_hash, state, batch_id, turn_id,
                          state_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetReviewNote :one
SELECT * FROM review_notes WHERE id = ?;

-- name: ListReviewNotesByTask :many
SELECT * FROM review_notes WHERE task_id = ? ORDER BY created_at, id;

-- name: ListReviewNotesByState :many
SELECT * FROM review_notes WHERE state = ? ORDER BY created_at, id;

-- name: CountOpenReviewNotesByTask :one
SELECT COUNT(*) FROM review_notes WHERE task_id = ? AND state IN ('open', 'outdated');

-- name: UpdateReviewNote :execrows
UPDATE review_notes SET text = ?, state = ?, batch_id = ?, turn_id = ?, state_at = ?, version = version + 1
WHERE id = ? AND version = ?;

-- name: DeleteReviewNote :execrows
DELETE FROM review_notes WHERE id = ? AND state IN ('open', 'outdated');
