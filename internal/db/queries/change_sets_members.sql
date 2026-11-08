-- name: ListChangeSetsByStatus :many
SELECT * FROM change_sets WHERE status = ? ORDER BY created_at, id;

-- name: GetChangeSetByProposal :one
SELECT cs.* FROM change_sets cs JOIN proposals p ON p.change_set_id = cs.id WHERE p.id = ?;
