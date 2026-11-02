package proposals

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// LandingReader reads what the merge and removal executors observe. *git.Git
// implements it.
type LandingReader interface {
	Status(ctx context.Context, dir string) (git.Status, error)
	WorktreeList(ctx context.Context, repoDir string) ([]git.Worktree, error)
}

// WorktreeMarks moves a worktree row along active -> merged -> removing ->
// removed (docs/design/landing.md R8). Each method runs in the transaction tx
// that also holds the audit entry of the change (invariant 8); it is
// idempotent, monotone and an expected-status update, so a wrong status is a
// no-op, not an error. The db package supplies the real one.
type WorktreeMarks interface {
	MarkMerged(ctx context.Context, tx *db.Tx, id core.WorktreeID) error
	// MarkRemoving remembers the staged leaf name.
	MarkRemoving(ctx context.Context, tx *db.Tx, id core.WorktreeID, stagedLeaf string) error
	MarkRemoved(ctx context.Context, tx *db.Tx, id core.WorktreeID) error
}

// LiveProbe says whether something is still running in a worktree.
type LiveProbe interface {
	// TaskLive: the worktree's task has a running agent.
	TaskLive(ctx context.Context, task core.TaskID) (bool, error)
	// BuildLive: a build or verification is queued or running in the worktree.
	BuildLive(ctx context.Context, wt core.WorktreeID) (bool, error)
}
