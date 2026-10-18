-- name: GetSetting :one
SELECT * FROM settings WHERE key = ?;

-- name: InsertSetting :exec
INSERT INTO settings (key, value, version) VALUES (?, ?, 0);

-- name: UpdateSetting :execrows
UPDATE settings SET value = ?, version = version + 1 WHERE key = ? AND version = ?;
