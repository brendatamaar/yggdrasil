-- name: CreateTask :exec
INSERT INTO tasks (id, project_id, parent_task_id, title, prompt, scope, mode, agent, model,
                   status, session_id, created_at, updated_at, finished_at,
                   source_kind, source_instance, source_project, source_number, dedup_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- The internal repo-setup draft belongs to the new-repo flow, not to the board
-- or any ordinary list.
-- name: ListTasksByProject :many
SELECT * FROM tasks WHERE project_id = ? AND source_kind <> 'repo_setup' ORDER BY created_at, id;

-- The first draft imported for an issue in this project, if any (AC-05 dedup).
-- name: GetTaskByDedupKey :one
SELECT * FROM tasks WHERE project_id = ? AND dedup_key = ?;

-- The setup draft that owns a reserved repo.
-- name: GetSetupTaskByRepo :one
SELECT t.* FROM tasks t JOIN task_repos tr ON tr.task_id = t.id
WHERE tr.repo_id = ? AND t.source_kind = 'repo_setup';

-- name: ListSubTasks :many
SELECT * FROM tasks WHERE parent_task_id = ? ORDER BY created_at, id;

-- name: UpdateTaskStatus :execrows
UPDATE tasks SET status = ?, updated_at = ?, finished_at = ?, version = version + 1 WHERE id = ? AND version = ?;

-- name: UpdateTaskSession :execrows
UPDATE tasks SET session_id = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?;

-- A task only gets a repo that is still tracked, READY and belongs to the
-- task's project; checked inside the write transaction (invariant 6). Zero
-- rows means the repo was untracked, is only reserved, or isn't the project's.
-- name: AddTaskRepo :execrows
INSERT INTO task_repos (task_id, repo_id)
SELECT sqlc.arg(task_id), r.id FROM repos r
WHERE r.id = sqlc.arg(repo_id) AND r.untracked_at IS NULL AND r.readiness = 'ready'
  AND r.project_id = (SELECT t.project_id FROM tasks t WHERE t.id = sqlc.arg(task_id));

-- The narrow setup path: only a RESERVED repo, only for a repo-setup draft of
-- the same project, inside the reservation transaction. Ordinary AddTaskRepo
-- never accepts a reserved repo.
-- name: AddSetupTaskRepo :execrows
INSERT INTO task_repos (task_id, repo_id)
SELECT sqlc.arg(task_id), r.id FROM repos r
WHERE r.id = sqlc.arg(repo_id) AND r.untracked_at IS NULL AND r.readiness = 'reserved'
  AND r.project_id = (SELECT t.project_id FROM tasks t WHERE t.id = sqlc.arg(task_id) AND t.source_kind = 'repo_setup');

-- name: CountTrackedProject :one
SELECT count(*) FROM projects WHERE id = ? AND untracked_at IS NULL;

-- name: ListTaskRepos :many
SELECT repo_id FROM task_repos WHERE task_id = ? ORDER BY repo_id;

-- name: AddTaskDependency :exec
INSERT INTO task_dependencies (task_id, depends_on_task_id) VALUES (?, ?);

-- name: ListTaskDependencies :many
SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ? ORDER BY depends_on_task_id;
