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
UPDATE tasks SET status = ?, updated_at = ?, finished_at = ? WHERE id = ?;

-- name: UpdateTaskSession :execrows
UPDATE tasks SET session_id = ?, updated_at = ? WHERE id = ?;

-- name: AddTaskRepo :exec
INSERT INTO task_repos (task_id, repo_id) VALUES (?, ?);

-- name: ListTaskRepos :many
SELECT repo_id FROM task_repos WHERE task_id = ? ORDER BY repo_id;

-- name: AddTaskDependency :exec
INSERT INTO task_dependencies (task_id, depends_on_task_id) VALUES (?, ?);

-- name: ListTaskDependencies :many
SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ? ORDER BY depends_on_task_id;
