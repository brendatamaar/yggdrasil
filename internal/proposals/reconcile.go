package proposals

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// recover reconciles every proposal that was Confirmed when the last run
// ended. It sends Recovered to Step, which answers with a read-only Reconcile
// command and never an Execute (P6). Nothing runs on its own after a crash:
// the user sees Executed or a Retry button.
func (s *Service) recover(ctx context.Context) error {
	recs, err := s.cfg.Store.ListProposalRecords(ctx, core.ProposalConfirmed)
	if err != nil {
		return fmt.Errorf("recover proposals: %w", err)
	}
	for _, r := range recs {
		_, cmds := coreprop.Step(r.Proposal, coreprop.Recovered{Now: s.now()})
		s.dispatch(r.Proposal, cmds)
	}
	return nil
}

func (s *Service) runReconcile(p coreprop.Proposal, cmd coreprop.Reconcile) {
	ctx := s.ctx
	if ctx.Err() != nil {
		return
	}
	out, res := coreprop.ReconcileUnknown, coreprop.Result{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Logger.Error("proposal reconcile panicked", "proposal", p.ID, "panic", s.redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
				out = coreprop.ReconcileUnknown
			}
		}()
		out, res = s.reconcile(ctx, p)
	}()
	if ctx.Err() != nil {
		return
	}
	in := coreprop.Reconciled{Attempt: cmd.Attempt, Outcome: out, Result: res, Now: s.now()}
	if err := s.applyInput(ctx, p.ID, in); err != nil {
		s.cfg.Logger.Error("could not record a reconcile", "proposal", p.ID, "error", s.redact(err.Error()))
	}
}

// reconcile checks, read-only, whether a crashed execution landed.
func (s *Service) reconcile(ctx context.Context, p coreprop.Proposal) (coreprop.ReconcileOutcome, coreprop.Result) {
	unknown := func() (coreprop.ReconcileOutcome, coreprop.Result) {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()
	switch a := p.Action.(type) {
	case coreprop.Commit:
		wd, stop := s.open(ctx, p, a.Where, false)
		if stop != nil {
			return unknown()
		}
		info, err := s.cfg.Git.HeadInfo(ctx, wd.dir)
		if err != nil {
			return unknown()
		}
		if info.Parent == a.ExpectHead && info.Message == strings.TrimSpace(a.Message) {
			return coreprop.ReconcileDone, coreprop.Result{CommitID: info.ID}
		}
		return coreprop.ReconcileNotDone, coreprop.Result{}
	case coreprop.Push:
		return s.reconcilePush(ctx, p, a.Where, a.Branch, a.Remote, a.ExpectHead, false)
	case coreprop.MergeRequest:
		return s.reconcilePush(ctx, p, a.Where, a.Branch, a.Remote, a.ExpectHead, true)
	case coreprop.AgentFile:
		wd, stop := s.open(ctx, p, a.Where, false)
		if stop != nil {
			return unknown()
		}
		full, err := insideRoot(wd.dir, string(a.Path))
		if err != nil {
			return unknown()
		}
		cur, err := currentFileHash(full)
		if err != nil {
			return unknown()
		}
		if cur == FileHash([]byte(a.Content)) {
			return coreprop.ReconcileDone, coreprop.Result{Note: "Wrote " + string(a.Path) + "."}
		}
		return coreprop.ReconcileNotDone, coreprop.Result{}
	case coreprop.DeleteFiles:
		wd, stop := s.open(ctx, p, a.Where, false)
		if stop != nil {
			return unknown()
		}
		var moved []coreprop.RelPath
		for _, rel := range a.Paths {
			dest, ok := movedAlready(wd.dir, string(p.ID), string(rel))
			if !ok {
				return coreprop.ReconcileNotDone, coreprop.Result{}
			}
			moved = append(moved, coreprop.RelPath(dest))
		}
		return coreprop.ReconcileDone, coreprop.Result{MovedPaths: moved, Note: "Moved the files into " + toDeleteDir + ". Nothing was deleted."}
	case coreprop.MergeLocal:
		return s.reconcileMerge(ctx, p, a)
	case coreprop.RemoveWorktree:
		return s.reconcileRemove(ctx, p, a)
	}
	return unknown() // Install: the build may or may not have run
}

// reconcilePush: the branch on the remote equals the proposed commit means the
// push landed. For a merge request GitLab is not asked, so anything else is
// Unknown rather than NotDone.
func (s *Service) reconcilePush(ctx context.Context, p coreprop.Proposal, where coreprop.Target, branch, remote, expect string, mr bool) (coreprop.ReconcileOutcome, coreprop.Result) {
	wd, stop := s.open(ctx, p, where, false)
	if stop != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	tip, err := s.cfg.Git.LsRemote(ctx, wd.dir, remote, branch)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	if tip != "" && tip == expect {
		note := alreadyPushed
		if mr {
			note += " Check the repo page for the merge request."
		}
		return coreprop.ReconcileDone, coreprop.Result{Note: note}
	}
	if mr {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	return coreprop.ReconcileNotDone, coreprop.Result{}
}
