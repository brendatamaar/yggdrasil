-- name: CreateProject :exec
INSERT INTO projects (id, name, path, created_at) VALUES (?, ?, ?, ?);

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;
