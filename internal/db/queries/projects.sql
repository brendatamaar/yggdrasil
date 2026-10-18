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
