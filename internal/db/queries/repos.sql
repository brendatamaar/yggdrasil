-- name: CreateRepo :exec
INSERT INTO repos (id, project_id, name, path, default_branch, trusted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetRepo :one
SELECT * FROM repos WHERE id = ?;

-- name: ListReposByProject :many
SELECT * FROM repos WHERE project_id = ? ORDER BY name;

-- name: SetRepoTrusted :execrows
UPDATE repos SET trusted = ? WHERE id = ?;
