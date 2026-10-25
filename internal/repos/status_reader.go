package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

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
//
// A commit never changes, so the reader remembers the last commit of each repo
// by its HEAD oid (which git status already reports) and skips git log while
// the oid is the same. The first read of a repo and any new HEAD still run
// git log.
type GitReader struct {
	g gitReads

	mu    sync.Mutex
	cache map[string]cachedCommit
}

// cachedCommit is the last commit read for one repo folder.
type cachedCommit struct {
	oid    string
	commit git.Commit
}

// NewGitReader returns a reader over g.
func NewGitReader(g gitReads) *GitReader {
	return &GitReader{g: g, cache: map[string]cachedCommit{}}
}

// Retain forgets the cached commits of every folder not in keep. The status
// service calls it when the set of tracked repos changes, so an untracked
// repo leaves nothing behind.
func (r *GitReader) Retain(keep []corerepos.CleanPath) {
	want := make(map[string]struct{}, len(keep))
	for _, p := range keep {
		want[p.String()] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for dir := range r.cache {
		if _, ok := want[dir]; !ok {
			delete(r.cache, dir)
		}
	}
}

func (r *GitReader) cached(dir, oid string) (git.Commit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[dir]
	if !ok || c.oid != oid {
		return git.Commit{}, false
	}
	return c.commit, true
}

// direct reads the commit from the object store when the git wrapper offers
// it (git.Git does), which saves a git process per repo. When it cannot answer
// for sure, the caller runs git log.
func (r *GitReader) direct(dir, oid string) (git.Commit, bool) {
	d, ok := r.g.(interface {
		HeadCommit(dir, oid string) (git.Commit, bool)
	})
	if !ok {
		return git.Commit{}, false
	}
	return d.HeadCommit(dir, oid)
}

func (r *GitReader) remember(dir, oid string, c git.Commit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[dir] = cachedCommit{oid: oid, commit: c}
}

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
		if c, ok := r.cached(dir, st.OID); ok {
			last = &c
		} else if c, ok := r.direct(dir, st.OID); ok {
			last = &c
			r.remember(dir, st.OID, c)
		} else {
			cs, err := r.g.Log(ctx, dir, 1)
			if err != nil {
				return corerepos.Snapshot{}, classifyGit(ctx, err), fmt.Errorf("read last commit of %s: %w", dir, err)
			}
			if len(cs) > 0 {
				last = &cs[0]
				// Only a log that answers for this oid is cached; HEAD may
				// have moved between the two git calls.
				if cs[0].Hash == st.OID {
					r.remember(dir, st.OID, cs[0])
				}
			}
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
