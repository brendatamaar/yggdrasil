-- name: CreateBuild :exec
INSERT INTO builds (id, worktree_id, task_id, target, status, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetBuild :one
SELECT * FROM builds WHERE id = ?;

-- name: ListBuildsByTask :many
SELECT * FROM builds WHERE task_id = ? ORDER BY created_at, id;

-- name: CreateBuildStep :exec
INSERT INTO build_steps (build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at, argv, attempt, outcome)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListBuildSteps :many
SELECT * FROM build_steps WHERE build_id = ? ORDER BY idx;

-- name: InsertBuildRow :exec
INSERT INTO builds (id, worktree_id, task_id, target, status, created_at, finished_at, version, actor,
                    started_at, result_step, preset_id, cancelled_at, artifacts_collected_at)
VALUES (?, ?, NULL, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?);

-- name: UpdateBuildRow :execrows
UPDATE builds SET status = ?, finished_at = ?, started_at = ?, result_step = ?, cancelled_at = ?,
                  artifacts_collected_at = ?, version = version + 1
WHERE id = ? AND version = ?;

-- name: InsertBuildStepRow :exec
INSERT INTO build_steps (build_id, idx, repo_id, name, status, exit_code, log_path, started_at, finished_at,
                         argv, attempt, outcome, start_failure, log_truncated)
VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteBuildStepRows :exec
DELETE FROM build_steps WHERE build_id = ?;

-- name: InsertBuildArtifactRow :exec
INSERT INTO build_artifacts (build_id, repo_id, path, size) VALUES (?, ?, ?, ?);

-- name: DeleteBuildArtifactRows :exec
DELETE FROM build_artifacts WHERE build_id = ?;

-- name: ListBuildArtifactRows :many
SELECT * FROM build_artifacts WHERE build_id = ? ORDER BY path;

-- name: ListQueuedBuildRefs :many
SELECT id, created_at FROM builds WHERE status = 'queued' ORDER BY created_at, id;

-- name: CountRunningBuilds :one
SELECT COUNT(*) FROM builds WHERE status = 'running';

-- name: ListActiveBuildIDs :many
SELECT id FROM builds WHERE status = 'running' ORDER BY created_at, id;

-- name: ListBuildIDsFiltered :many
SELECT id FROM builds
WHERE (sqlc.narg(status) IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(repo_id) IS NULL OR id IN (SELECT build_id FROM build_steps WHERE repo_id = sqlc.narg(repo_id)))
  AND (sqlc.narg(project_id) IS NULL OR id IN (
        SELECT bs.build_id FROM build_steps bs JOIN repos r ON r.id = bs.repo_id WHERE r.project_id = sqlc.narg(project_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(max_rows);
