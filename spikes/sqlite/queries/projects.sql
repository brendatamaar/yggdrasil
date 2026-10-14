-- name: CreateProject :one
INSERT INTO projects (name) VALUES (?) RETURNING *;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE name = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;
