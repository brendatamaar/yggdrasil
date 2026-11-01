package git

import (
	"context"
	"path/filepath"
)

// WorktreePruneArgv is the argument list of WorktreePrune (after "git").
func WorktreePruneArgv() []string {
	return []string{"-c", "core.fsmonitor=false", "worktree", "prune"}
}

// WorktreePrune drops git's records of linked worktrees whose folder is gone.
// It touches only git's own bookkeeping under .git/worktrees; it removes no
// working files, and no worktree whose folder still exists.
func (g *Git) WorktreePrune(ctx context.Context, repoDir string) error {
	_, err := g.execOK(ctx, repoDir, "stale worktree records", nil, WorktreePruneArgv()...)
	return err
}

// samePath compares two cleaned paths exactly. A caller on a case-insensitive
// file system should pass the path as git lists it (platform.CanonicalPath);
// a difference in case only makes OtherPrunable answer true, which skips the
// prune and is the safe side.
func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

// OtherPrunable reports whether the list has a prunable entry at a path other
// than path (compared as cleaned paths). The landing executor prunes only when
// this is false, so it never drops the record of a worktree it did not remove.
func OtherPrunable(list []Worktree, path string) bool {
	want := filepath.Clean(path)
	for _, w := range list {
		if w.Prunable && !samePath(w.Path, want) {
			return true
		}
	}
	return false
}
