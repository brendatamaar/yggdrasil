-- name: GetRepoBuildSettings :one
SELECT * FROM repo_build_settings WHERE repo_id = ?;

-- name: UpsertRepoBuildSettings :exec
INSERT INTO repo_build_settings (repo_id, verify, full_tests, bootstrap, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id) DO UPDATE SET
    verify = excluded.verify, full_tests = excluded.full_tests, bootstrap = excluded.bootstrap,
    updated_at = excluded.updated_at, version = repo_build_settings.version + 1;

-- name: DeleteRepoBuildSettings :execrows
DELETE FROM repo_build_settings WHERE repo_id = ?;
