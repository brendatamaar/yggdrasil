package proposals

import (
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func refusedBadFacts(t *testing.T, err error, what string) {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != coreprop.RefuseBadFacts || r.UserMessage() == "" {
		t.Fatalf("%s: err = %v, want a bad_facts refusal with a message", what, err)
	}
}

func TestPushAndMRBranchMustBeTheWorktreeBranchAtCreate(t *testing.T) {
	w := newWorld(t)
	head := w.head(w.wtDir)
	wt := coreprop.InWorktree{ID: wtID}
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.Push{Where: wt, Branch: "ygg/other", Remote: "origin", ExpectHead: head}})
	refusedBadFacts(t, err, "push of another branch")
	_, err = w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.MergeRequest{Where: wt, Branch: "ygg/other", Remote: "origin", TargetBranch: "main", Title: "t", ExpectHead: head}})
	refusedBadFacts(t, err, "merge request of another branch")
}

func TestPushBranchMustBeTheCurrentBranchOfTheMainCheckout(t *testing.T) {
	w := newWorld(t)
	w.git.setCurrentBranch(w.mainDir, "ygg/in-main")
	head := w.head(w.mainDir)
	where := coreprop.MainCheckout{Repo: w.repo.ID}
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.Push{Where: where, Branch: "ygg/elsewhere", Remote: "origin", ExpectHead: head}})
	refusedBadFacts(t, err, "main checkout push of a non-current branch")
	v := w.create(coreprop.Push{Where: where, Branch: "ygg/in-main", Remote: "origin", ExpectHead: head})
	// The user switched branches in the main checkout after the proposal.
	w.git.setCurrentBranch(w.mainDir, "ygg/switched")
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || got.Retryable || len(w.git.pushes) != 0 {
		t.Fatalf("failure = %+v, pushes %d", got, len(w.git.pushes))
	}
}

func TestPushFailsWhenTheWorktreeBranchChangedAfterCreate(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: w.head(w.wtDir)})
	w.res.mu.Lock()
	info := w.res.wts[wtID]
	info.Branch = "ygg/renamed"
	w.res.wts[wtID] = info
	w.res.mu.Unlock()
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || got.Retryable || len(w.git.pushes) != 0 {
		t.Fatalf("failure = %+v, pushes %d", got, len(w.git.pushes))
	}
}
