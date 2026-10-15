-- name: InsertAuditEntry :exec
INSERT INTO audit_log (id, at, actor, action, subject, detail) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAuditEntries :many
SELECT * FROM audit_log ORDER BY at DESC, id DESC LIMIT ?;

-- name: ListAuditEntriesBySubject :many
SELECT * FROM audit_log WHERE subject = ? ORDER BY at DESC, id DESC LIMIT ?;
