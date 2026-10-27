package proposals

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// BranchReader is the part of the git wrapper that reads the checked-out
// branch of a folder. *git.Git implements it; New requires it.
type BranchReader interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
}

// StagedReader lists the files staged in the index. *git.Git implements it;
// New requires it.
type StagedReader interface {
	StagedPaths(ctx context.Context, dir string) ([]string, error)
}

const wrongBranchMsg = "A push or merge request may only send the branch of this task (for the main checkout, the branch that is checked out now). Name that branch."

var errWrongBranch = errors.New("branch is not the task branch")

// checkBranchBinding proves that branch is the one this target owns: the task
// worktree's branch, or the current branch of the main checkout (invariants 6
// and 11). dir is the folder the target resolved to.
func (s *Service) checkBranchBinding(ctx context.Context, where coreprop.Target, dir, branch string) error {
	var want string
	switch w := where.(type) {
	case coreprop.InWorktree:
		wt, err := s.cfg.Resolver.Worktree(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("read worktree %s: %w", w.ID, err)
		}
		want = wt.Branch
	case coreprop.MainCheckout:
		br, ok := s.cfg.Git.(BranchReader)
		if !ok {
			return errors.New("the git wrapper cannot read the current branch")
		}
		cur, err := br.CurrentBranch(ctx, dir)
		if err != nil {
			return fmt.Errorf("read the current branch: %w", err)
		}
		want = cur
	}
	if want == "" || want != branch {
		return errWrongBranch
	}
	return nil
}

// view is viewOf plus what the confirm dialog needs and the action hash does
// not cover: the repo name, and for a pending push or merge request the host it
// goes to (a "Remote: host" line). A lookup that fails only leaves them out.
func (s *Service) view(ctx context.Context, p coreprop.Proposal) View {
	v := viewOf(p)
	if info, err := s.cfg.Resolver.Repo(ctx, p.Repo); err == nil {
		v.RepoName = info.Name
	}
	if v.Status != core.ProposalPending {
		return v
	}
	var where coreprop.Target
	var remote string
	switch a := p.Action.(type) {
	case coreprop.Push:
		where, remote = a.Where, a.Remote
	case coreprop.MergeRequest:
		where, remote = a.Where, a.Remote
	default:
		return v
	}
	dir, err := s.dirOf(ctx, where)
	if err != nil || dir == "" {
		return v
	}
	url, err := s.cfg.Git.RemoteURL(ctx, dir, remote)
	if err != nil {
		return v
	}
	if host, err := checkRemoteURL(url); err == nil && host != "" {
		v.Remote = host
		v.Lines = append(v.Lines, "Remote: "+host)
	}
	return v
}

// pushBranch returns the branch a Push or MergeRequest names ("" for others).
func pushBranch(a coreprop.Action) (string, bool) {
	switch v := a.(type) {
	case coreprop.Push:
		return v.Branch, true
	case coreprop.MergeRequest:
		return v.Branch, true
	}
	return "", false
}
