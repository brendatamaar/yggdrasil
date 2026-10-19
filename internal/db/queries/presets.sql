-- name: InsertBuildPreset :exec
INSERT INTO build_presets (id, project_id, name, steps, created_at, version) VALUES (?, ?, ?, ?, ?, 0);

-- name: UpdateBuildPreset :execrows
UPDATE build_presets SET name = ?, steps = ?, version = version + 1 WHERE id = ? AND version = ?;

-- name: GetBuildPreset :one
SELECT * FROM build_presets WHERE id = ?;

-- name: ListBuildPresets :many
SELECT * FROM build_presets WHERE project_id = ? ORDER BY name, id;
