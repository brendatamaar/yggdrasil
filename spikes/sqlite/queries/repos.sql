-- name: CreateRepo :one
INSERT INTO repos (project_id, name, path) VALUES (?, ?, ?) RETURNING *;

-- name: ListReposByProject :many
SELECT * FROM repos WHERE project_id = ? ORDER BY name;

-- name: CountRepos :one
SELECT count(*) FROM repos;
