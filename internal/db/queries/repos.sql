-- name: CreateRepo :exec
INSERT INTO repos (id, project_id, name, path, rel_path, default_branch, trusted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- Untracked repos (untracked_at set) are invisible to GetRepo and
-- ListReposByProject, which the API lists and task creation use (SF-02).
-- GetRepoIncludingUntracked serves history views (builds of an untracked repo).
--
-- RESERVED repos (readiness = 'reserved', a new repo that is only named) are
-- invisible to EVERY ordinary query below: agents, MCP, builds, terminals, dev
-- servers, status and the API all reach repos through these reads, so the
-- filter is here in SQL, not in the UI (docs/design/phase-6-newrepos.md). Only
-- the Reserved* queries of phase6.sql see them.

-- name: GetRepo :one
SELECT * FROM repos WHERE id = ? AND untracked_at IS NULL AND readiness = 'ready';

-- name: GetRepoIncludingUntracked :one
SELECT * FROM repos WHERE id = ? AND readiness = 'ready';

-- name: ListReposByProject :many
SELECT * FROM repos WHERE project_id = ? AND untracked_at IS NULL AND readiness = 'ready' ORDER BY name;

-- Workspace loading and decisions of the repo-tracking step. They see
-- untracked rows on purpose (see projects.sql).

-- name: ListAllRepos :many
SELECT * FROM repos WHERE readiness = 'ready' ORDER BY id;

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
UPDATE repos SET trusted = ? WHERE id = ? AND untracked_at IS NULL AND readiness = 'ready';

-- Reserved repos (NR-01): the narrow setup path. A reservation only names a
-- tracked project's future child folder; it creates nothing on disk.

-- name: CreateReservedRepo :exec
INSERT INTO repos (id, project_id, name, path, rel_path, default_branch, trusted, readiness, created_at)
VALUES (?, ?, ?, ?, ?, '', 0, 'reserved', ?);

-- name: GetReservedRepo :one
SELECT * FROM repos WHERE id = ? AND readiness = 'reserved' AND untracked_at IS NULL;

-- name: ListReservedRepos :many
SELECT * FROM repos WHERE readiness = 'reserved' AND untracked_at IS NULL ORDER BY id;

-- A reserved repo becomes ready exactly once, from the finalize stage.
-- name: MarkRepoReady :execrows
UPDATE repos SET readiness = 'ready', default_branch = ? WHERE id = ? AND readiness = 'reserved' AND untracked_at IS NULL;
