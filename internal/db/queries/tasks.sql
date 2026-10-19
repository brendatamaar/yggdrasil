-- name: CreateTask :exec
INSERT INTO tasks (id, project_id, parent_task_id, title, prompt, scope, mode, agent, model,
                   status, session_id, created_at, updated_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: ListTasksByProject :many
SELECT * FROM tasks WHERE project_id = ? ORDER BY created_at, id;

-- name: ListSubTasks :many
SELECT * FROM tasks WHERE parent_task_id = ? ORDER BY created_at, id;

-- name: UpdateTaskStatus :execrows
UPDATE tasks SET status = ?, updated_at = ?, finished_at = ?, version = version + 1 WHERE id = ? AND version = ?;

-- name: UpdateTaskSession :execrows
UPDATE tasks SET session_id = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?;

-- A task only gets a repo that is still tracked and belongs to the task's
-- project; checked inside the write transaction (invariant 6). Zero rows
-- means the repo was untracked (or isn't the project's) meanwhile.
-- name: AddTaskRepo :execrows
INSERT INTO task_repos (task_id, repo_id)
SELECT sqlc.arg(task_id), r.id FROM repos r
WHERE r.id = sqlc.arg(repo_id) AND r.untracked_at IS NULL
  AND r.project_id = (SELECT t.project_id FROM tasks t WHERE t.id = sqlc.arg(task_id));

-- name: CountTrackedProject :one
SELECT count(*) FROM projects WHERE id = ? AND untracked_at IS NULL;

-- name: ListTaskRepos :many
SELECT repo_id FROM task_repos WHERE task_id = ? ORDER BY repo_id;

-- name: AddTaskDependency :exec
INSERT INTO task_dependencies (task_id, depends_on_task_id) VALUES (?, ?);

-- name: ListTaskDependencies :many
SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ? ORDER BY depends_on_task_id;
