-- name: UpdateChangeSetRun :execrows
UPDATE change_sets SET status = ?, merge_order = ?, run_result = ?, version = version + 1
WHERE id = ? AND version = ?;
