package git

import "context"

// LandingGit is the part of git that lands a reviewed branch into a main
// checkout (RV-05, docs/design/landing.md). *Git implements it; gittest.FakeLanding
// is the in-memory fake, and both pass gittest.LandingGitContract.
//
// It has no push, reset, delete or removal: none exists to call (invariant 1).
type LandingGit interface {
	// MergeNoFF merges the exact commit branchHead with a merge commit (hooks
	// off, nothing pushed) and returns the new HEAD. Errors: *MergeConflictError,
	// ErrIdentityMissing, *Error.
	MergeNoFF(ctx context.Context, dir, branchHead, message string) (string, error)
	// MergeAbort ends a merge in progress; with none it does nothing.
	MergeAbort(ctx context.Context, dir string) error
	// MergeInProgress reports an unfinished merge and its MERGE_HEAD.
	MergeInProgress(ctx context.Context, dir string) (bool, string, error)
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error)
	// BranchHead returns a local branch's commit, or "" when it does not exist.
	BranchHead(ctx context.Context, dir, branch string) (string, error)
	// MergeTreeWriteTree predicts a merge without touching any working tree.
	// ErrMergeTreeUnsupported on git older than 2.38.
	MergeTreeWriteTree(ctx context.Context, dir, ours, theirs string) (Prediction, error)
	// WorktreePrune drops git's records of worktrees whose folder is gone.
	WorktreePrune(ctx context.Context, repoDir string) error
}

// BaseDiffer reads what an agent changed in a worktree relative to its base
// commit (RV-01, RV-06). It is the DiffSource of the change-set service and
// the DiffProbe of verification.
type BaseDiffer interface {
	DiffAgainstBase(ctx context.Context, dir, base string, opts BaseDiffOptions) (BaseDiff, error)
	// HasChanges is DiffAgainstBase without the patches.
	HasChanges(ctx context.Context, dir, base string) (bool, error)
}

var (
	_ LandingGit = (*Git)(nil)
	_ BaseDiffer = (*Git)(nil)
)
