package proposals

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/landing"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

const (
	notAvailableMsg = "Merging locally and removing worktrees are not set up in this build of Yggdrasil. Nothing was changed."
	maxFilesInMsg   = 10
	maxLoopSteps    = 24
)

// landingReady reports whether the landing capabilities are wired.
func (s *Service) landingReady(needLive bool) bool {
	c := s.cfg
	if c.Landing == nil || c.Reader == nil || c.Marks == nil {
		return false
	}
	return !needLive || (c.Live != nil && c.WorktreeRoot != "")
}

// execMerge runs a confirmed merge_local: it interprets landing.StepMerge. It
// holds the repo's main-checkout lock (open), re-checks every precondition
// through the Observe command, merges with --no-ff and never pushes, resets or
// forces (invariants 1 and 2; docs/design/landing.md).
func (s *Service) execMerge(ctx context.Context, p coreprop.Proposal, a coreprop.MergeLocal, attempt int) outcome {
	if !s.landingReady(false) {
		return failed(coreprop.FailPrecondition, false, "%s", notAvailableMsg)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()
	wd, stop := s.open(ctx, p, a.Where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	wt, out := s.landingWorktree(ctx, p, a.Worktree)
	if out != nil {
		return *out
	}
	if wt.Branch != a.Branch {
		return failed(coreprop.FailPrecondition, false, "The worktree is now on another branch than the one proposed. Ask for a new proposal.")
	}
	mode := landing.ModeFresh
	if attempt > 1 {
		mode = landing.ModeRecovering
	}
	r := &mergeRun{
		s: s, p: p, wt: wt, mode: mode,
		req: landing.MergeReq{Repo: wd.repo, Worktree: a.Worktree, Branch: a.Branch, ExpectBranchHead: a.ExpectBranchHead,
			TargetBranch: a.TargetBranch, Title: a.Title, Path: wd.dir},
	}
	return r.run(ctx)
}

// landingWorktree looks up the worktree of a landing proposal and checks that it
// belongs to the proposal's task and repo.
func (s *Service) landingWorktree(ctx context.Context, p coreprop.Proposal, id core.WorktreeID) (WorktreeInfo, *outcome) {
	wt, err := s.cfg.Resolver.Worktree(ctx, id)
	if err != nil {
		o := failed(coreprop.FailExec, true, "Yggdrasil could not look up the worktree. Try again.")
		if errors.Is(err, db.ErrNotFound) {
			o = failed(coreprop.FailPrecondition, false, "The worktree for this change no longer exists.")
		}
		return WorktreeInfo{}, &o
	}
	if wt.Repo != p.Repo || wt.Task != p.Task {
		o := failed(coreprop.FailPrecondition, false, "The worktree does not belong to this task and repo.")
		return WorktreeInfo{}, &o
	}
	return wt, nil
}

// mergeRun is the state of one interpretation of StepMerge.
type mergeRun struct {
	s    *Service
	p    coreprop.Proposal
	wt   WorktreeInfo
	mode landing.Mode
	req  landing.MergeReq

	head    string // HEAD at the last observation
	started bool   // RunMerge has been issued: the main checkout may have changed
}

// detached is a context that survives shutdown, for the steps that must finish
// once a merge has started, with its own time limit.
func (r *mergeRun) detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), r.s.cfg.ExecTimeout)
}

func (r *mergeRun) run(ctx context.Context) outcome {
	var st landing.MergeState = landing.MergeIdle{}
	var in landing.MergeInput = landing.MergeStart{Req: r.req, Mode: r.mode, Now: r.s.now()}
	for range maxLoopSteps {
		var cmds []landing.MergeCommand
		st, cmds = landing.StepMerge(st, in)
		in = nil
		var batch []landing.MergeCommand
		for _, c := range cmds {
			switch v := c.(type) {
			case landing.Observe:
				o, err := r.observe(ctx)
				if err != nil {
					return r.observeFailed(ctx, err)
				}
				in = o
			case landing.Predict:
				in = r.predict(ctx)
			case landing.RunMerge:
				r.started = true
				in = r.runMerge(ctx, v)
			case landing.Abort:
				in = r.abort(ctx)
			case landing.MarkWorktreeMerged, landing.WriteAudit:
				batch = append(batch, c)
			}
		}
		if len(batch) > 0 {
			if err := r.persist(ctx, batch); err != nil {
				r.s.cfg.Logger.Error("could not record a landing step", "proposal", r.p.ID, "error", r.s.redact(err.Error()))
			}
		}
		if out, ok := r.finished(st); ok {
			return out
		}
		if in == nil {
			return failed(coreprop.FailExec, true, "Yggdrasil lost track of the merge. Check the main checkout with git status, then retry.")
		}
	}
	return failed(coreprop.FailExec, true, "The merge did not settle. Check the main checkout with git status, then retry.")
}

// persist writes the worktree mark and the audit entry of one step in one
// transaction (invariant 8).
func (r *mergeRun) persist(ctx context.Context, cmds []landing.MergeCommand) error {
	ctx = context.WithoutCancel(ctx)
	return r.s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		for _, c := range cmds {
			switch v := c.(type) {
			case landing.MarkWorktreeMerged:
				if err := r.s.cfg.Marks.MarkMerged(ctx, tx, v.Worktree); err != nil {
					return fmt.Errorf("mark worktree merged: %w", err)
				}
			case landing.WriteAudit:
				if err := r.s.landingAudit(ctx, tx, r.p, v); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// landingAudit appends a terminal landing outcome. The detail never holds a
// path, a message or a branch name.
func (s *Service) landingAudit(ctx context.Context, tx *db.Tx, p coreprop.Proposal, w landing.WriteAudit) error {
	e, err := audit.Prepare(core.AuditEntry{
		Actor: "system", Action: w.Action, Subject: "proposal:" + string(p.ID),
		Detail: []byte(fmt.Sprintf(`{"kind":%q,"task":%q,"repo":%q,"outcome":%q}`, p.Action.Kind(), p.Task, p.Repo, w.Outcome)),
	}, s.cfg.Redactor, core.AuditID(s.cfg.NewID("aud")), s.now())
	if err != nil {
		return fmt.Errorf("prepare audit entry: %w", err)
	}
	return tx.AppendAudit(ctx, e)
}

// observe reads the facts of the merge, retrying once on a git error.
func (r *mergeRun) observe(ctx context.Context) (landing.Observed, error) {
	if r.started {
		var cancel context.CancelFunc
		ctx, cancel = r.detached(ctx)
		defer cancel()
	}
	o, err := r.observeOnce(ctx)
	if err != nil && ctx.Err() == nil {
		o, err = r.observeOnce(ctx)
	}
	return o, err
}

func (r *mergeRun) observeOnce(ctx context.Context) (landing.Observed, error) {
	s, lg, rd := r.s, r.s.cfg.Landing, r.s.cfg.Reader
	o := landing.Observed{Tracked: s.repoTracked(ctx, r.req.Repo)}
	st, err := rd.Status(ctx, r.req.Path)
	if err != nil {
		return o, fmt.Errorf("read the main checkout: %w", err)
	}
	o.Clean = st.Staged+st.Unstaged+st.Untracked+st.Conflicted == 0
	o.DetachedHead, o.CurrentBranch, o.Head = st.Detached, st.Branch, st.OID
	r.head = st.OID
	if o.MergeInProgress, o.MergeHead, err = lg.MergeInProgress(ctx, r.req.Path); err != nil {
		return o, fmt.Errorf("read the merge state: %w", err)
	}
	if o.BranchHead, err = lg.BranchHead(ctx, r.req.Path, r.req.Branch); err != nil {
		return o, fmt.Errorf("read the branch: %w", err)
	}
	if o.BranchHead != "" && o.Head != "" {
		if o.AlreadyMerged, err = lg.IsAncestor(ctx, r.req.Path, o.BranchHead, o.Head); err != nil {
			return o, fmt.Errorf("compare the branch: %w", err)
		}
	}
	wst, err := rd.Status(ctx, r.wt.Path)
	if err != nil {
		return o, fmt.Errorf("read the worktree: %w", err)
	}
	o.WorktreeClean = wst.Staged+wst.Unstaged+wst.Untracked+wst.Conflicted == 0
	o.Now = s.now()
	return o, nil
}

func (r *mergeRun) observeFailed(ctx context.Context, err error) outcome {
	if !r.started {
		return readFailure("read the repo", err)
	}
	// A merge was started and the tree cannot be read: nobody can say what state
	// it is in. Record it and ask the user to look.
	_ = r.persist(ctx, []landing.MergeCommand{landing.WriteAudit{Action: "landing.merge", Outcome: "needs_human"}})
	return failed(coreprop.FailInterrupted, true, "Yggdrasil could not check the main checkout after starting the merge. Run git status there, run `git merge --abort` if a merge is in progress, then retry.")
}

func (r *mergeRun) predict(ctx context.Context) landing.Predicted {
	p, err := r.s.cfg.Landing.MergeTreeWriteTree(ctx, r.req.Path, r.head, r.req.ExpectBranchHead)
	res := landing.PredictResult(landing.PredictClean{})
	switch {
	case errors.Is(err, git.ErrMergeTreeUnsupported):
		res = landing.PredictUnsupported{}
	case err != nil:
		res = landing.PredictError{Msg: git.UserMessage(err)}
	case !p.Clean:
		files := make([]landing.RelPath, len(p.Files))
		for i, f := range p.Files {
			files[i] = landing.RelPath(f)
		}
		res = landing.PredictConflicts{Files: files}
	}
	return landing.Predicted{Result: res, Now: r.s.now()}
}

// runMerge is the one place a merge starts. It compares HEAD before and after:
// git reports success without a new commit when the branch was already in.
func (r *mergeRun) runMerge(ctx context.Context, c landing.RunMerge) landing.MergeInput {
	ctx, cancel := r.detached(ctx)
	defer cancel()
	before, err := r.s.cfg.Git.Head(ctx, r.req.Path)
	if err != nil {
		return landing.MergeErrored{Reason: landing.FailExec, Msg: git.UserMessage(err), Now: r.s.now()}
	}
	after, err := r.s.cfg.Landing.MergeNoFF(ctx, r.req.Path, c.Req.ExpectBranchHead, c.Message)
	var ce *git.MergeConflictError
	switch {
	case errors.As(err, &ce):
		files := make([]landing.RelPath, len(ce.Files))
		for i, f := range ce.Files {
			files[i] = landing.RelPath(f)
		}
		return landing.MergeConflicted{Files: files, Now: r.s.now()}
	case errors.Is(err, git.ErrIdentityMissing):
		return landing.MergeErrored{Reason: landing.FailIdentityMissing, Msg: git.UserMessage(err), Now: r.s.now()}
	case err != nil:
		return landing.MergeErrored{Reason: landing.FailExec, Msg: git.UserMessage(err), Now: r.s.now()}
	case after == before:
		return landing.MergeErrored{Reason: landing.FailExec, Msg: "git made no merge commit", Now: r.s.now()}
	}
	return landing.MergeDone{Commit: after, Now: r.s.now()}
}

// abort undoes a merge. The core only emits it after our own RunMerge, or for
// our own interrupted merge found at the start of a recovering run.
func (r *mergeRun) abort(ctx context.Context) landing.MergeInput {
	ctx, cancel := r.detached(ctx)
	defer cancel()
	if err := r.s.cfg.Landing.MergeAbort(ctx, r.req.Path); err != nil {
		return landing.AbortErrored{Msg: git.UserMessage(err), Now: r.s.now()}
	}
	return landing.AbortDone{Now: r.s.now()}
}

// finished maps a terminal state to the outcome of the proposal.
func (r *mergeRun) finished(st landing.MergeState) (outcome, bool) {
	switch v := st.(type) {
	case landing.Merged:
		return done(coreprop.Result{CommitID: v.Commit, Note: fmt.Sprintf("Merged %s into %s with a merge commit. Nothing was pushed.", r.req.Branch, r.req.TargetBranch)}), true
	case landing.AlreadyMerged:
		return done(coreprop.Result{CommitID: v.Head, Note: fmt.Sprintf("%s was already part of %s, so nothing was merged.", r.req.Branch, r.req.TargetBranch)}), true
	case landing.MergeRefused:
		return refusedMerge(v.Reason, r.req), true
	case landing.Conflicted:
		return failed(coreprop.FailExec, false, "The branch conflicts with %s in %s. Nothing was merged and the main checkout is unchanged. Resolve the conflicts in the worktree (for example by asking the agent to merge %s into its branch), then propose the merge again.",
			r.req.TargetBranch, fileList(v.Files), r.req.TargetBranch), true
	case landing.MergeFailed:
		if v.Reason == landing.FailIdentityMissing {
			return failed(coreprop.FailExec, v.Retryable, "Git does not know who you are, so it could not write the merge commit. Set git user.name and user.email, then retry. Nothing was changed."), true
		}
		kind := coreprop.FailExec
		if v.Reason == landing.FailInterrupted {
			kind = coreprop.FailInterrupted
		}
		return failed(kind, v.Retryable, "The merge did not complete and the main checkout was left clean. Retry, or check the repo with git status."), true
	case landing.NeedsHuman:
		return failed(coreprop.FailExec, false, "%s", v.Why), true
	}
	return outcome{}, false
}

func refusedMerge(reason landing.RefuseReason, req landing.MergeReq) outcome {
	switch reason {
	case landing.RefuseUntrackedRepo:
		return failed(coreprop.FailRepoUntracked, false, "This repo is no longer tracked, so nothing was merged. Track the repo again, then ask for a new proposal.")
	case landing.RefuseMergeInProgress:
		return failed(coreprop.FailPrecondition, true, "A merge is already in progress in the main checkout. Finish or abort it there, then retry. Nothing was changed.")
	case landing.RefuseWrongBranch:
		return failed(coreprop.FailPrecondition, true, "The main checkout is not on %s. Switch it back, then retry. Nothing was changed.", req.TargetBranch)
	case landing.RefuseMainDirty:
		return failed(coreprop.FailPrecondition, true, "The main checkout has uncommitted or untracked files. Commit or move them, then retry. Nothing was changed.")
	case landing.RefuseStale:
		return failed(coreprop.FailStale, false, "The branch has new commits since this was proposed. Ask for a new proposal. Nothing was changed.")
	case landing.RefuseBranchMissing:
		return failed(coreprop.FailPrecondition, false, "The branch no longer exists. Nothing was changed.")
	case landing.RefuseWorktreeDirty:
		return failed(coreprop.FailPrecondition, true, "The worktree has uncommitted or untracked changes, so what was reviewed is not what would merge. Commit them (through a commit proposal) or discard them, then retry. Nothing was changed.")
	}
	return failed(coreprop.FailPrecondition, false, "The merge was refused. Nothing was changed.")
}

// fileList names up to maxFilesInMsg files, with control characters removed.
func fileList(files []landing.RelPath) string {
	var names []string
	for i, f := range files {
		if i == maxFilesInMsg {
			names = append(names, fmt.Sprintf("and %d more", len(files)-maxFilesInMsg))
			break
		}
		names = append(names, plainName(string(f)))
	}
	if len(names) == 0 {
		return "some files"
	}
	return strings.Join(names, ", ")
}

func plainName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// reconcileMerge decides, read only, whether a merge that was running at a crash
// landed: the proposed branch head being part of HEAD, with no merge in
// progress, means it did. Anything else is "not done"; a Retry then starts a
// recovering run, which aborts only a merge that is ours.
func (s *Service) reconcileMerge(ctx context.Context, p coreprop.Proposal, a coreprop.MergeLocal) (coreprop.ReconcileOutcome, coreprop.Result) {
	if !s.landingReady(false) {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	wd, stop := s.open(ctx, p, a.Where, false)
	if stop != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	st, err := s.cfg.Reader.Status(ctx, wd.dir)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	inProgress, _, err := s.cfg.Landing.MergeInProgress(ctx, wd.dir)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	bh, err := s.cfg.Landing.BranchHead(ctx, wd.dir, a.Branch)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	if !inProgress && bh != "" && bh == a.ExpectBranchHead && st.OID != "" {
		in, err := s.cfg.Landing.IsAncestor(ctx, wd.dir, bh, st.OID)
		if err != nil {
			return coreprop.ReconcileUnknown, coreprop.Result{}
		}
		if in {
			return coreprop.ReconcileDone, coreprop.Result{CommitID: st.OID, Note: fmt.Sprintf("%s is part of %s. Nothing was pushed.", a.Branch, a.TargetBranch)}
		}
	}
	return coreprop.ReconcileNotDone, coreprop.Result{}
}
