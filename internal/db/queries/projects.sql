-- name: CreateProject :exec
INSERT INTO projects (id, name, path, created_at) VALUES (?, ?, ?, ?);

-- Untracked projects (untracked_at set) are invisible to every read below
-- except GetProjectAny, which only the store's own writes use (SF-02).

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? AND untracked_at IS NULL;

-- name: GetProjectAny :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects WHERE untracked_at IS NULL ORDER BY name;

-- Workspace loading and decisions of the repo-tracking step
-- (docs/design/repo-tracking.md). The queries below see untracked rows on
-- purpose: only the workspace store in internal/db uses them, never the API
-- reads.

-- name: ListAllProjects :many
SELECT * FROM projects ORDER BY id;

-- name: BumpProjectVersion :execrows
UPDATE projects SET version = version + 1 WHERE id = ? AND version = ?;

-- name: CountProject :one
SELECT count(*) FROM projects WHERE id = ?;

-- name: SetProjectName :exec
UPDATE projects SET name = ? WHERE id = ?;

-- name: SetProjectUntracked :exec
UPDATE projects SET untracked_at = ? WHERE id = ?;

-- name: ClearProjectUntracked :exec
UPDATE projects SET untracked_at = NULL, name = ? WHERE id = ?;
