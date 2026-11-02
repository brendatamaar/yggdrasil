package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

// Go-only wiring (excluded from tygo): the landing service (RV-05, RV-06) and
// the worktree differ (RV-01) over the store, git and the proposals service.
// Neither changes anything on disk: landing only creates pending proposals.

// LandingStore is what the landing and diff adapters read from the database
// (*db.Store satisfies it). GetRepo answers only for tracked repos
// (invariant 6).
type LandingStore interface {
	GetWorktree(ctx context.Context, id core.WorktreeID) (core.Worktree, error)
	ListWorktrees(ctx context.Context, task core.TaskID) ([]core.Worktree, error)
	GetWorktreeBaseCommit(ctx context.Context, id core.WorktreeID) (string, error)
	GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
}

// LandingGit is what the adapters read from git (*git.Git satisfies it).
type LandingGit interface {
	BranchHead(ctx context.Context, dir, branch string) (string, error)
	CurrentBranch(ctx context.Context, dir string) (string, error)
	DiffAgainstBase(ctx context.Context, dir, base string, opts git.BaseDiffOptions) (git.BaseDiff, error)
}

// ProposalCreator is the part of *proposals.Service the landing adapter calls.
type ProposalCreator interface {
	Create(ctx context.Context, req proposals.CreateRequest) (proposals.View, error)
}

type landingAdapter struct {
	store LandingStore
	git   LandingGit
	props ProposalCreator
}

// NewLandingService serves the merge-local and remove-worktree endpoints. Both
// only create a pending proposal; the user confirms it like any other.
func NewLandingService(store LandingStore, g LandingGit, props ProposalCreator) LandingService {
	return &landingAdapter{store: store, git: g, props: props}
}

// worktree finds a worktree that still exists, with its tracked repo.
func (a *landingAdapter) worktree(ctx context.Context, id core.WorktreeID) (core.Worktree, core.Repo, error) {
	missing := func(err error) error {
		return apperr.New(apperr.NotFound, "That worktree does not exist (any more).", err)
	}
	w, err := a.store.GetWorktree(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return core.Worktree{}, core.Repo{}, missing(err)
	}
	if err != nil {
		return core.Worktree{}, core.Repo{}, fmt.Errorf("read worktree %s: %w", id, err)
	}
	if w.Status == core.WorktreeRemoved || w.Status == core.WorktreeRemoving {
		return core.Worktree{}, core.Repo{}, missing(fmt.Errorf("worktree %s is %s", id, w.Status))
	}
	r, err := a.store.GetRepo(ctx, w.RepoID)
	if errors.Is(err, db.ErrNotFound) {
		return core.Worktree{}, core.Repo{}, apperr.New(apperr.NotFound, "The repo of this worktree is not tracked any more.", err)
	}
	if err != nil {
		return core.Worktree{}, core.Repo{}, fmt.Errorf("read repo %s: %w", w.RepoID, err)
	}
	return w, r, nil
}

func (a *landingAdapter) ProposeMergeLocal(ctx context.Context, id core.WorktreeID, req MergeLocalRequest) (ProposalDTO, error) {
	w, repo, err := a.worktree(ctx, id)
	if err != nil {
		return ProposalDTO{}, err
	}
	head, err := a.git.BranchHead(ctx, repo.Path, w.Branch)
	if err != nil {
		return ProposalDTO{}, apperr.New(apperr.Unavailable, "Yggdrasil could not read the branch of this worktree. Try again.", err)
	}
	if head == "" {
		return ProposalDTO{}, apperr.New(apperr.Conflict, "The branch of this worktree does not exist any more, so there is nothing to merge.", errors.New("branch missing"))
	}
	target, err := a.git.CurrentBranch(ctx, repo.Path)
	if err != nil {
		return ProposalDTO{}, apperr.New(apperr.Unavailable, "Yggdrasil could not read the current branch of the repo. Try again.", err)
	}
	title := req.Title
	if title == "" {
		title = "Merge " + w.Branch
	}
	action := coreprop.MergeLocal{
		Where: coreprop.MainCheckout{Repo: w.RepoID}, Worktree: w.ID, Branch: w.Branch,
		TargetBranch: target, ExpectBranchHead: head, Title: title,
	}
	if err := action.Validate(); err != nil {
		return ProposalDTO{}, apperr.New(apperr.Invalid, "This change cannot be merged as it is: "+err.Error()+".", err)
	}
	return a.create(ctx, w.TaskID, action, fmt.Sprintf("Merge %s into %s in %s", w.Branch, target, repo.Name))
}

func (a *landingAdapter) ProposeRemoveWorktree(ctx context.Context, id core.WorktreeID) (ProposalDTO, error) {
	w, repo, err := a.worktree(ctx, id)
	if err != nil {
		return ProposalDTO{}, err
	}
	return a.create(ctx, w.TaskID, coreprop.RemoveWorktree{Worktree: w.ID}, "Remove the worktree of "+repo.Name)
}

func (a *landingAdapter) create(ctx context.Context, task core.TaskID, action coreprop.Action, summary string) (ProposalDTO, error) {
	v, err := a.props.Create(ctx, proposals.CreateRequest{Task: task, Action: action, Summary: summary, Source: coreprop.UserSource{}})
	if err != nil {
		return ProposalDTO{}, proposalErr(err)
	}
	return proposalDTO(v), nil
}

type worktreeDiffAdapter struct {
	store LandingStore
	git   LandingGit
}

// NewWorktreeDiffer serves GET /api/tasks/{id}/repos/{repo}/diff: what the
// agent changed in the task's active worktree of a tracked repo, against the
// commit the worktree started from.
func NewWorktreeDiffer(store LandingStore, g LandingGit) WorktreeDiffer {
	return &worktreeDiffAdapter{store: store, git: g}
}

func (a *worktreeDiffAdapter) DiffWorktree(ctx context.Context, task core.TaskID, repo core.RepoID) (git.BaseDiff, error) {
	none := func(err error) error {
		return apperr.New(apperr.NotFound, "This task has no worktree for that repo (any more).", err)
	}
	if _, err := a.store.GetRepo(ctx, repo); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return git.BaseDiff{}, apperr.New(apperr.NotFound, "That repo is not tracked.", err)
		}
		return git.BaseDiff{}, fmt.Errorf("read repo %s: %w", repo, err)
	}
	wts, err := a.store.ListWorktrees(ctx, task)
	if err != nil {
		return git.BaseDiff{}, fmt.Errorf("list worktrees of task %s: %w", task, err)
	}
	for _, w := range wts {
		if w.RepoID != repo || w.Status != core.WorktreeActive {
			continue
		}
		base, err := a.store.GetWorktreeBaseCommit(ctx, w.ID)
		if err != nil {
			return git.BaseDiff{}, fmt.Errorf("read the base commit of worktree %s: %w", w.ID, err)
		}
		bd, err := a.git.DiffAgainstBase(ctx, w.Path, base, git.BaseDiffOptions{})
		if err != nil {
			return git.BaseDiff{}, apperr.New(apperr.Unavailable, "Yggdrasil could not read the changes of this worktree. Try again.", err)
		}
		return bd, nil
	}
	return git.BaseDiff{}, none(fmt.Errorf("no active worktree of task %s in repo %s", task, repo))
}
