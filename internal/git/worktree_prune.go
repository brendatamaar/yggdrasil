package git

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
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

// samePath compares two paths the way the disk would: the closest existing
// parent is replaced by the spelling the disk reports (long names, on-disk
// case), and case is ignored where the file system ignores it. A folder that is
// already gone keeps its own last segments.
func samePath(a, b string) bool {
	x, y := normPath(a), normPath(b)
	if platform.PathsCaseInsensitive {
		return strings.EqualFold(x, y)
	}
	return x == y
}

func normPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if c, err := platform.CanonicalPath(cur); err == nil {
			return filepath.Join(c, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

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
