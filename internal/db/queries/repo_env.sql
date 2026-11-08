-- name: UpsertRepoEnvProfile :exec
-- Writes only the profile columns; the compose columns of an existing row are not touched.
INSERT INTO repo_env_settings (repo_id, profile_paths, agent_copy, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id) DO UPDATE SET
    profile_paths = excluded.profile_paths,
    agent_copy = excluded.agent_copy,
    updated_at = excluded.updated_at;

-- name: GetRepoEnvSettings :one
SELECT * FROM repo_env_settings WHERE repo_id = ?;

-- name: UpsertRepoEnvSettings :exec
INSERT INTO repo_env_settings (repo_id, profile_paths, agent_copy, compose_enabled, compose_file,
                               compose_confirmed_hash, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id) DO UPDATE SET
    profile_paths = excluded.profile_paths,
    agent_copy = excluded.agent_copy,
    compose_enabled = excluded.compose_enabled,
    compose_file = excluded.compose_file,
    compose_confirmed_hash = excluded.compose_confirmed_hash,
    updated_at = excluded.updated_at;
