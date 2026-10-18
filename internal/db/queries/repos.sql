-- name: CreateRepo :exec
INSERT INTO repos (id, project_id, name, path, rel_path, default_branch, trusted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- Untracked repos (untracked_at set) are invisible to GetRepo and
-- ListReposByProject, which the API lists and task creation use (SF-02).
-- GetRepoIncludingUntracked serves history views (builds of an untracked repo).

-- name: GetRepo :one
SELECT * FROM repos WHERE id = ? AND untracked_at IS NULL;

-- name: GetRepoIncludingUntracked :one
SELECT * FROM repos WHERE id = ?;

-- name: ListReposByProject :many
SELECT * FROM repos WHERE project_id = ? AND untracked_at IS NULL ORDER BY name;

-- Workspace loading and decisions of the repo-tracking step. They see
-- untracked rows on purpose (see projects.sql).

-- name: ListAllRepos :many
SELECT * FROM repos ORDER BY id;

-- name: SetRepoUntracked :execrows
UPDATE repos SET untracked_at = ? WHERE id = ? AND project_id = ?;

-- A restored repo is never trusted: its agent config may have changed (SF-09).
-- name: ClearRepoUntracked :execrows
UPDATE repos SET untracked_at = NULL, trusted = 0 WHERE id = ? AND project_id = ?;

-- Repos whose main checkout has a queued or running build (Cancelling is a
-- running build with cancelled_at set).
-- name: ListBusyBuildRepos :many
SELECT DISTINCT bs.repo_id FROM build_steps bs
JOIN builds b ON b.id = bs.build_id
WHERE b.target = 'main' AND b.status IN ('queued', 'running')
ORDER BY bs.repo_id;

-- Repos in scope of a task that is queued, running or waiting for the user.
-- name: ListBusyTaskRepos :many
SELECT DISTINCT tr.repo_id FROM task_repos tr
JOIN tasks t ON t.id = tr.task_id
WHERE t.status IN ('queued', 'running', 'needs_you')
ORDER BY tr.repo_id;

-- name: SetRepoTrusted :execrows
UPDATE repos SET trusted = ? WHERE id = ? AND untracked_at IS NULL;
