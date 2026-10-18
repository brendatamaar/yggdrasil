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

-- name: SetRepoTrusted :execrows
UPDATE repos SET trusted = ? WHERE id = ?;
