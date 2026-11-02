package proposals

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/landing"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// execRemove runs a confirmed remove_worktree: it interprets landing.StepRemove.
// The worktree folder is renamed into <worktree root>/.removing and only that
// staged folder is deleted, by platform.RemoveStaged. There is no git worktree
// remove, no force, and the branch is never touched (invariant 1).
func (s *Service) execRemove(ctx context.Context, p coreprop.Proposal, a coreprop.RemoveWorktree) outcome {
	if !s.landingReady(true) {
		return failed(coreprop.FailPrecondition, false, "%s", notAvailableMsg)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()
	// open re-checks that the repo is tracked (invariant 6).
	if _, stop := s.open(ctx, p, coreprop.InWorktree{ID: a.Worktree}, false); stop != nil {
		return *stop
	}
	wt, out := s.landingWorktree(ctx, p, a.Worktree)
	if out != nil {
		return *out
	}
	repo, err := s.cfg.Resolver.Repo(ctx, p.Repo)
	if err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not look up the repo. Try again.")
	}
	r := &removeRun{s: s, p: p, wt: wt, repoDir: repo.Path}
	r.root, r.rootErr = platform.ResolveRoot(s.cfg.WorktreeRoot)
	r.path, r.pathErr = platform.ResolveRoot(wt.Path)
	if r.rootErr != nil {
		return failed(coreprop.FailPrecondition, false, "The folder for task worktrees cannot be read, so nothing was removed.")
	}
	name := wt.Name
	if name == "" {
		name = filepath.Base(wt.Path)
	}
	reqPath := r.path
	if r.pathErr != nil {
		reqPath = wt.Path // refused as outside the root below
	}
	r.req = landing.RemoveReq{Worktree: a.Worktree, Repo: p.Repo, Path: reqPath, Branch: wt.Branch, Root: r.root, Name: name, StagedLeaf: wt.StagedLeaf}
	return r.run(ctx)
}

type removeRun struct {
	s       *Service
	p       coreprop.Proposal
	wt      WorktreeInfo
	repoDir string
	req     landing.RemoveReq

	root, path       string
	rootErr, pathErr error
	stageID          string
	lstat            func(string) (fs.FileInfo, error) // os.Lstat; replaced in tests
}

func (r *removeRun) stat(p string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(p)
	}
	return os.Lstat(p)
}

const notRecordedMsg = "The worktree folder was removed on disk, but Yggdrasil could not record it. Retry; nothing more will be deleted."

func (r *removeRun) run(ctx context.Context) outcome {
	var st landing.RemoveState = landing.RemoveIdle{}
	r.stageID = r.s.cfg.StageID()
	var in landing.RemoveInput = landing.RemoveStart{Req: r.req, StageID: r.stageID, Now: r.s.now()}
	for range maxLoopSteps {
		var cmds []landing.RemoveCommand
		st, cmds = landing.StepRemove(st, in)
		in = nil
		var batch []landing.RemoveCommand
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			err := r.persist(ctx, batch)
			if err != nil {
				err = r.persist(ctx, batch) // once more: the effect already ran
			}
			batch = nil
			if err != nil {
				r.s.cfg.Logger.Error("could not record a removal step", "proposal", r.p.ID, "error", r.s.redact(err.Error()))
			}
			return err
		}
		for _, c := range cmds {
			switch v := c.(type) {
			case landing.ObserveWorktree:
				o, err := r.observe(ctx)
				if err != nil {
					return readFailure("read the worktree", err)
				}
				in = o
			case landing.MoveToStaging:
				in = r.move(v)
			case landing.DeleteStaged:
				if flush() != nil { // MarkRemoving, if any, comes before the first deletion
					return failed(coreprop.FailExec, true, "%s", notRecordedMsg)
				}
				in = r.deleteStaged(v)
			case landing.PruneWorktrees:
				in = r.prune(ctx)
			case landing.MarkRemoving, landing.MarkRemoved, landing.WriteAudit:
				batch = append(batch, c)
			}
		}
		if flush() != nil {
			return failed(coreprop.FailExec, true, "%s", notRecordedMsg)
		}
		if out, ok := r.finished(st); ok {
			return out
		}
		if in == nil {
			return failed(coreprop.FailExec, true, "Yggdrasil lost track of the removal. Retry.")
		}
	}
	return failed(coreprop.FailExec, true, "The removal did not settle. Retry.")
}

func (r *removeRun) persist(ctx context.Context, cmds []landing.RemoveCommand) error {
	ctx = context.WithoutCancel(ctx)
	return r.s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		for _, c := range cmds {
			var err error
			switch v := c.(type) {
			case landing.MarkRemoving:
				if err = r.s.cfg.Marks.MarkRemoving(ctx, tx, v.Worktree, v.Staged.Leaf); err == nil {
					err = r.s.landingAudit(ctx, tx, r.p, landing.WriteAudit{Action: "landing.remove", Outcome: "removing"})
				}
			case landing.MarkRemoved:
				err = r.s.cfg.Marks.MarkRemoved(ctx, tx, v.Worktree)
			case landing.WriteAudit:
				err = r.s.landingAudit(ctx, tx, r.p, v)
			}
			if err != nil {
				return fmt.Errorf("record removal: %w", err)
			}
		}
		return nil
	})
}

// underRoot: the worktree is strictly inside the worktree root and not inside
// the staging folder. Both are resolved spellings.
func (r *removeRun) underRoot() bool {
	if r.pathErr != nil {
		return false
	}
	rel, err := filepath.Rel(r.root, r.path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return rel != landing.StagingDir && !strings.HasPrefix(rel, landing.StagingDir+string(filepath.Separator))
}

func sameFile(a, b string) bool {
	if platform.FoldCase() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (r *removeRun) observe(ctx context.Context) (landing.RemoveObserved, error) {
	s := r.s
	o := landing.RemoveObserved{UnderRoot: r.underRoot(), Clean: true}
	if main, err := platform.ResolveRoot(r.repoDir); err == nil && r.pathErr == nil {
		o.IsMain = sameFile(main, r.path)
	} else if r.pathErr == nil && sameFile(filepath.Clean(r.repoDir), r.path) {
		o.IsMain = true
	}
	cur, err := s.cfg.Resolver.Worktree(ctx, r.req.Worktree)
	if err != nil {
		return o, fmt.Errorf("read the worktree row: %w", err)
	}
	o.RowStatus = cur.Status
	// Only "does not exist" means absent; any other error leaves the fact
	// unreadable, so the core refuses (RemoveUnreadable).
	unreadable := false
	switch _, statErr := r.stat(r.req.Path); {
	case statErr == nil:
		o.PathExists = true
	case !errors.Is(statErr, fs.ErrNotExist):
		o.PathExists, unreadable = true, true
	}
	var stagedErr error
	o.Staged, o.StagedExists, stagedErr = r.findStaged()
	if stagedErr != nil {
		o.PathExists, unreadable = true, true
		o.StagedExists = false
	}
	if unreadable {
		o.StatusReadable = false
	} else if o.PathExists {
		st, err := s.cfg.Reader.Status(ctx, r.req.Path)
		o.StatusReadable = err == nil
		o.Clean = err == nil && st.Staged+st.Unstaged+st.Untracked+st.Conflicted == 0
	}
	if o.TaskLive, err = s.cfg.Live.TaskLive(ctx, r.p.Task); err != nil {
		return o, fmt.Errorf("check the task: %w", err)
	}
	if o.BuildLive, err = s.cfg.Live.BuildLive(ctx, r.req.Worktree); err != nil {
		return o, fmt.Errorf("check builds: %w", err)
	}
	// Prune only when ours is the only prunable record; unknown means "do not".
	if list, lerr := s.cfg.Reader.WorktreeList(ctx, r.repoDir); lerr != nil {
		o.OtherPrunable = true
	} else {
		o.OtherPrunable = git.OtherPrunable(list, r.req.Path)
	}
	o.Now = s.now()
	return o, nil
}

// stagedLeaf is the one leaf this removal may own: the remembered one, else the
// one planned from this worktree's name and this run's stage ID.
func (r *removeRun) stagedLeaf() (landing.StagePath, error) {
	if r.req.StagedLeaf != "" {
		return landing.StagePath{Root: r.req.Root, Leaf: r.req.StagedLeaf}, nil
	}
	return landing.StagingPath(r.req.Root, r.req.Name, r.stageID)
}

// findStaged checks that exact staged folder; it never scans the staging
// folder. Only "does not exist" means nothing is staged.
func (r *removeRun) findStaged() (landing.StagePath, bool, error) {
	p, err := r.stagedLeaf()
	if err != nil {
		return landing.StagePath{}, false, nil
	}
	fi, err := r.stat(stagedDir(p))
	switch {
	case err == nil && fi.IsDir():
		return p, true, nil
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return landing.StagePath{}, false, nil
	}
	return landing.StagePath{}, false, err
}

func stagedDir(p landing.StagePath) string {
	return filepath.Join(p.Root, landing.StagingDir, p.Leaf)
}

// move renames the worktree folder into the staging folder. A failure changes
// nothing; the holder names are looked up only now, because that takes time.
func (r *removeRun) move(c landing.MoveToStaging) landing.RemoveInput {
	pl := r.s.cfg.Platform
	if err := os.MkdirAll(filepath.Join(c.To.Root, landing.StagingDir), 0o750); err != nil {
		return landing.MoveFailed{Class: landing.LockClass(pl.ClassifyLock(err)), Now: r.s.now()}
	}
	err := pl.MoveDir(c.Req.Path, stagedDir(c.To))
	if err == nil {
		return landing.Moved{Now: r.s.now()}
	}
	class := pl.ClassifyLock(err)
	var me *platform.MoveError
	if errors.As(err, &me) {
		class = me.Class
	}
	return landing.MoveFailed{Class: landing.LockClass(class), Holders: pl.LockHolders(c.Req.Path), Now: r.s.now()}
}

func (r *removeRun) deleteStaged(c landing.DeleteStaged) landing.RemoveInput {
	pl := r.s.cfg.Platform
	dir := stagedDir(c.Staged)
	rep, err := pl.RemoveStaged(c.Staged.Root, dir)
	if err == nil && rep.Remaining == 0 {
		return landing.Deleted{Now: r.s.now()}
	}
	class := rep.Class
	if class == "" || class == platform.LockOther {
		class = pl.ClassifyLock(errors.Join(err, rep.FirstErr))
	}
	return landing.DeleteFailed{Class: landing.LockClass(class), Holders: pl.LockHolders(dir), Remaining: rep.Remaining, Now: r.s.now()}
}

func (r *removeRun) prune(ctx context.Context) landing.RemoveInput {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.s.cfg.ExecTimeout)
	defer cancel()
	if err := r.s.cfg.Landing.WorktreePrune(ctx, r.repoDir); err != nil {
		return landing.PruneFailed{Msg: git.UserMessage(err), Now: r.s.now()}
	}
	return landing.Pruned{Now: r.s.now()}
}

func (r *removeRun) finished(st landing.RemoveState) (outcome, bool) {
	switch v := st.(type) {
	case landing.Removed:
		note := "The worktree folder was removed. The branch was kept."
		if v.Note != "" {
			note += " (" + v.Note + ".)"
		}
		return done(coreprop.Result{Note: note}), true
	case landing.RemoveRefused:
		return refusedRemove(v.Reason), true
	case landing.Locked:
		return failed(coreprop.FailExec, true, "%s", v.Message()), true
	case landing.Stuck:
		return failed(coreprop.FailExec, true, "%s", v.Message()), true
	case landing.RemoveFailed:
		return failed(coreprop.FailExec, v.Retryable, "%s", v.Reason), true
	}
	return outcome{}, false
}

func refusedRemove(reason landing.RemoveRefuse) outcome {
	const nothing = " Nothing was changed."
	switch reason {
	case landing.RemoveOutsideRoot:
		return failed(coreprop.FailPrecondition, false, "This folder is not a worktree that Yggdrasil created, so it will not be removed."+nothing)
	case landing.RemoveMainCheckout:
		return failed(coreprop.FailPrecondition, false, "This is a main checkout, which is never removed."+nothing)
	case landing.RemoveTaskRunning:
		return failed(coreprop.FailPrecondition, true, "An agent is still working in this worktree. Stop it first, then retry."+nothing)
	case landing.RemoveInUse:
		return failed(coreprop.FailPrecondition, true, "A build or check is running in this worktree. Wait for it or stop it, then retry."+nothing)
	case landing.RemoveDirty:
		return failed(coreprop.FailPrecondition, true, "The worktree has uncommitted or untracked files. Commit or move them out, then retry."+nothing)
	case landing.RemoveUnreadable:
		return failed(coreprop.FailPrecondition, false, "Yggdrasil could not read the state of this worktree (is the folder missing?), so it was not removed."+nothing)
	}
	return failed(coreprop.FailPrecondition, false, "The removal was refused."+nothing)
}

// reconcileRemove: with nothing left on disk, either at the path or staged, the
// removal is done; otherwise a Retry observes the staged folder and resumes.
func (s *Service) reconcileRemove(ctx context.Context, p coreprop.Proposal, a coreprop.RemoveWorktree) (coreprop.ReconcileOutcome, coreprop.Result) {
	unknown := coreprop.ReconcileUnknown
	if !s.landingReady(true) {
		return unknown, coreprop.Result{}
	}
	if _, stop := s.open(ctx, p, coreprop.InWorktree{ID: a.Worktree}, false); stop != nil {
		return unknown, coreprop.Result{}
	}
	wt, out := s.landingWorktree(ctx, p, a.Worktree)
	if out != nil {
		return unknown, coreprop.Result{}
	}
	root, err := platform.ResolveRoot(s.cfg.WorktreeRoot)
	if err != nil {
		return unknown, coreprop.Result{}
	}
	name := wt.Name
	if name == "" {
		name = filepath.Base(wt.Path)
	}
	r := &removeRun{req: landing.RemoveReq{Name: name, Root: root, StagedLeaf: wt.StagedLeaf}}
	_, staged, serr := r.findStaged()
	_, statErr := os.Lstat(wt.Path)
	if serr == nil && statErr != nil && errors.Is(statErr, os.ErrNotExist) && !staged && (wt.Status == core.WorktreeRemoving || wt.Status == core.WorktreeRemoved) {
		return coreprop.ReconcileDone, coreprop.Result{Note: "The worktree folder was removed. The branch was kept."}
	}
	return coreprop.ReconcileNotDone, coreprop.Result{}
}
