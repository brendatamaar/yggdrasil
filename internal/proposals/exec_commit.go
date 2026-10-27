package proposals

import (
	"context"
	"strings"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// execCommit commits exactly the named paths. It first proves the tree is the
// one the user reviewed: HEAD is ExpectHead and the paths hash to
// ExpectDiffHash. Hooks are off (internal/git). A second run after a crash
// finds HEAD moved and stops (Stale), so it cannot commit twice.
func (s *Service) execCommit(ctx context.Context, p coreprop.Proposal, a coreprop.Commit) outcome {
	wd, stop := s.open(ctx, p, a.Where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	if why := checkPaths(wd.dir, a.Paths, false); why != "" {
		return failed(coreprop.FailPrecondition, false, "%s", why)
	}
	head, err := s.cfg.Git.Head(ctx, wd.dir)
	if err != nil {
		return readFailure("read the current commit", err)
	}
	if head != a.ExpectHead {
		return failed(coreprop.FailStale, false, "The branch has new commits since this commit was proposed. Reject it and ask the agent for a new one.")
	}
	paths := pathStrings(a.Paths)
	if a.ExpectDiffHash != "" {
		dh, err := s.cfg.Git.DiffHash(ctx, wd.dir, paths)
		if err != nil {
			return readFailure("check the files", err)
		}
		if dh != a.ExpectDiffHash {
			return failed(coreprop.FailStale, false, "The files changed since this commit was proposed. Reject it and ask the agent for a new one.")
		}
	}
	if err := s.cfg.Git.Add(ctx, wd.dir, paths); err != nil {
		return gitFailure(err)
	}
	// Nothing but the declared files may be staged, and none of them protected.
	// The index is left as it is: this only fails (nothing is unstaged).
	staged, err := s.cfg.Git.(StagedReader).StagedPaths(ctx, wd.dir)
	if err != nil {
		return readFailure("check what is staged", err)
	}
	declared := map[string]bool{}
	for _, p := range paths {
		declared[p] = true
	}
	for _, sp := range staged {
		if !declared[sp] || coreprop.IsProtectedPath(coreprop.RelPath(sp)) {
			return failed(coreprop.FailPrecondition, false, "Files other than the ones in this proposal are staged in the repo, so nothing was committed. Yggdrasil left the staged files as they are: unstage the extra files yourself, then ask for a new proposal.")
		}
	}
	id, err := s.cfg.Git.Commit(ctx, wd.dir, paths, strings.TrimSpace(a.Message), a.ExpectHead)
	if err != nil {
		return gitFailure(err)
	}
	return done(coreprop.Result{CommitID: id})
}
