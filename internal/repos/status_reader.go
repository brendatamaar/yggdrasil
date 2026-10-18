package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// StatusReader reads the git state of one repo folder (docs/design/repo-status.md).
// When the read fails, err is not nil and kind says why (a domain outcome, not
// a crash). The contract suite is repostest.StatusReaderContract.
type StatusReader interface {
	Read(ctx context.Context, path corerepos.CleanPath) (snap corerepos.Snapshot, kind corerepos.ProblemKind, err error)
}

// gitReads is the part of *git.Git the reader uses.
type gitReads interface {
	Status(ctx context.Context, dir string) (git.Status, error)
	Log(ctx context.Context, dir string, n int) ([]git.Commit, error)
}

// GitReader is the StatusReader backed by the git CLI: os.Stat, git status and
// git log -1. Both git calls are read-only and run with --no-optional-locks.
type GitReader struct{ g gitReads }

// NewGitReader returns a reader over g.
func NewGitReader(g gitReads) *GitReader { return &GitReader{g: g} }

// Read implements StatusReader.
func (r *GitReader) Read(ctx context.Context, path corerepos.CleanPath) (corerepos.Snapshot, corerepos.ProblemKind, error) {
	dir := path.String()
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return corerepos.Snapshot{}, corerepos.ProblemMissing, fmt.Errorf("the folder %s does not exist", dir)
	case err != nil:
		return corerepos.Snapshot{}, corerepos.ProblemGitFailed, fmt.Errorf("check folder %s: %w", dir, err)
	case !fi.IsDir():
		return corerepos.Snapshot{}, corerepos.ProblemNotARepo, fmt.Errorf("%s is not a folder", dir)
	}
	st, err := r.g.Status(ctx, dir)
	if err != nil {
		return corerepos.Snapshot{}, classifyGit(ctx, err), fmt.Errorf("read status of %s: %w", dir, err)
	}
	var last *git.Commit
	if st.OID != "" {
		cs, err := r.g.Log(ctx, dir, 1)
		if err != nil {
			return corerepos.Snapshot{}, classifyGit(ctx, err), fmt.Errorf("read last commit of %s: %w", dir, err)
		}
		if len(cs) > 0 {
			last = &cs[0]
		}
	}
	snap, err := SnapshotFrom(st, last)
	if err != nil {
		return corerepos.Snapshot{}, corerepos.ProblemGitFailed, fmt.Errorf("read status of %s: %w", dir, err)
	}
	return snap, "", nil
}

// classifyGit maps a failed git call to a problem kind.
func classifyGit(ctx context.Context, err error) corerepos.ProblemKind {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return corerepos.ProblemTimedOut
	}
	var ge *git.Error
	if errors.As(err, &ge) && strings.Contains(strings.ToLower(ge.Stderr), "not a git repository") {
		return corerepos.ProblemNotARepo
	}
	return corerepos.ProblemGitFailed
}
