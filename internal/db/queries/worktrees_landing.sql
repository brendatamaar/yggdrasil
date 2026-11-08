-- Worktree status is monotone: active, merged, removing, removed
-- (docs/design/landing.md R8). Each update names the statuses it may start from
-- and reports the rows it changed, so a wrong expected status is a no-op.

-- name: MarkWorktreeMerged :execrows
UPDATE worktrees SET status = 'merged' WHERE id = ? AND status = 'active';

-- name: MarkWorktreeRemoving :execrows
UPDATE worktrees SET status = 'removing',
  staged_name = CASE WHEN status = 'removing' AND staged_name <> '' THEN staged_name ELSE ? END
WHERE id = ? AND status IN ('active', 'merged', 'removing');

-- name: MarkWorktreeRemoved :execrows
UPDATE worktrees SET status = 'removed' WHERE id = ? AND status = 'removing';

-- name: GetWorktreeStagedName :one
SELECT staged_name FROM worktrees WHERE id = ?;
