package proposals

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

// outcome is what an executor found: done with a result, or failed with a kind,
// a plain message and whether Retry may help.
type outcome struct {
	done      bool
	result    coreprop.Result
	kind      coreprop.FailKind
	msg       string
	retryable bool
}

func done(r coreprop.Result) outcome { return outcome{done: true, result: r} }

func failed(kind coreprop.FailKind, retryable bool, format string, a ...any) outcome {
	return outcome{kind: kind, retryable: retryable, msg: fmt.Sprintf(format, a...)}
}

// keyOf names the folder an action works in. One executor runs per key at a
// time, in the order proposals were confirmed.
func keyOf(a coreprop.Action) string {
	switch v := a.(type) {
	case coreprop.Commit:
		return targetKey(v.Where)
	case coreprop.Push:
		return targetKey(v.Where)
	case coreprop.MergeRequest:
		return targetKey(v.Where)
	case coreprop.AgentFile:
		return targetKey(v.Where)
	case coreprop.DeleteFiles:
		return targetKey(v.Where)
	case coreprop.Install:
		return "repo:" + string(v.Repo)
	case coreprop.MergeLocal, coreprop.RemoveWorktree:
		// Their executors land with PX1; until then they never run.
		return "landing"
	}
	return "other"
}

func targetKey(t coreprop.Target) string {
	switch w := t.(type) {
	case coreprop.InWorktree:
		return "wt:" + string(w.ID)
	case coreprop.MainCheckout:
		return "repo:" + string(w.Repo)
	}
	return "other"
}

// enqueue runs job after the jobs already queued under key. The worker is a
// goroutine of the service group; a closed service queues nothing.
func (s *Service) enqueue(key string, job func()) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.closed || s.ctx == nil {
		return
	}
	s.queues[key] = append(s.queues[key], job)
	if s.running[key] {
		return
	}
	s.running[key] = true
	s.group.Go(func() error {
		for {
			s.qmu.Lock()
			q := s.queues[key]
			if len(q) == 0 {
				delete(s.queues, key)
				delete(s.running, key)
				s.qmu.Unlock()
				return nil
			}
			next := q[0]
			s.queues[key] = q[1:]
			s.qmu.Unlock()
			s.safely(next)
		}
	})
}

// safely runs one queued job; a panic (in applyInput or Step, say) is logged
// and must not take the process down or stop the queue.
func (s *Service) safely(job func()) {
	defer func() {
		if r := recover(); r != nil {
			s.cfg.Logger.Error("proposal job panicked", "panic", s.redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
		}
	}()
	job()
}

// dispatch interprets the Execute and Reconcile commands of a step.
func (s *Service) dispatch(p coreprop.Proposal, cmds []coreprop.Command) {
	for _, c := range cmds {
		switch v := c.(type) {
		case coreprop.Execute:
			s.enqueue(keyOf(v.Action), func() { s.runExecute(p, v) })
		case coreprop.Reconcile:
			s.enqueue(keyOf(v.Action), func() { s.runReconcile(p, v) })
		}
	}
}

// runExecute runs one confirmed attempt and feeds the result back to Step. A
// panic becomes ExecFailed{Interrupted}. When the service is shutting down the
// proposal stays Confirmed and is reconciled at the next start.
func (s *Service) runExecute(p coreprop.Proposal, cmd coreprop.Execute) {
	ctx := s.ctx
	if ctx.Err() != nil {
		return
	}
	var out outcome
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Logger.Error("proposal executor panicked", "proposal", p.ID, "panic", s.redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
				out = failed(coreprop.FailInterrupted, true, "Something went wrong inside Yggdrasil while running this. Check the repo, then retry if needed.")
			}
		}()
		out = s.execute(ctx, p, cmd.Attempt)
	}()
	if ctx.Err() != nil {
		return
	}
	now := s.now()
	var in coreprop.Input = coreprop.ExecDone{Attempt: cmd.Attempt, Result: out.result, Now: now}
	if !out.done {
		in = coreprop.ExecFailed{Attempt: cmd.Attempt, Kind: out.kind, Message: s.redact(out.msg), Retryable: out.retryable, Now: now}
	}
	if err := s.applyInput(ctx, p.ID, in); err != nil {
		s.cfg.Logger.Error("could not record the result of a proposal", "proposal", p.ID, "error", s.redact(err.Error()))
	}
}

// applyInput loads the proposal, steps it with an input from an executor or a
// reconcile, saves state and audit in one transaction, then publishes and
// dispatches. A stale attempt or a decided proposal changes nothing.
func (s *Service) applyInput(ctx context.Context, id core.ProposalID, in coreprop.Input) error {
	for attempt := 0; attempt < 4; attempt++ {
		rec, err := s.cfg.Store.GetProposalRecord(ctx, id)
		if err != nil {
			return fmt.Errorf("load proposal %s: %w", id, err)
		}
		next, cmds := coreprop.Step(rec.Proposal, in)
		if len(cmds) == 0 {
			return nil
		}
		now := s.now()
		err = s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.SaveProposalRecord(ctx, next, rec.Version); err != nil {
				return err
			}
			return s.writeAudit(ctx, tx, next, cmds, "", now)
		})
		if errors.Is(err, db.ErrConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("save proposal %s: %w", id, err)
		}
		s.publishAll(next, cmds)
		s.dispatch(next, cmds)
		return nil
	}
	return fmt.Errorf("proposal %s changed too often", id)
}

// execute runs the executor of the action's kind.
func (s *Service) execute(ctx context.Context, p coreprop.Proposal, attempt int) outcome {
	switch a := p.Action.(type) {
	case coreprop.Commit:
		return s.execCommit(ctx, p, a)
	case coreprop.Push:
		return s.execPush(ctx, p, a)
	case coreprop.MergeRequest:
		return s.execMR(ctx, p, a)
	case coreprop.AgentFile:
		return s.execAgentFile(ctx, p, a)
	case coreprop.DeleteFiles:
		return s.execDelete(ctx, p, a)
	case coreprop.Install:
		return s.execInstall(ctx, p, a)
	case coreprop.MergeLocal, coreprop.RemoveWorktree:
		return failed(coreprop.FailPrecondition, false, "Merging locally and removing worktrees are not available yet. Nothing was changed.")
	}
	return failed(coreprop.FailPrecondition, false, "This kind of proposal cannot run yet.")
}

// workdir is the folder an executor works in and the lock it holds.
type workdir struct {
	dir     string
	repo    core.RepoID
	main    bool
	release func()
}

func (w workdir) done() {
	if w.release != nil {
		w.release()
	}
}

// open resolves where, re-checks that the repo is still tracked (invariant 6)
// and, for a main checkout, takes the shared lock (owner "proposal:<id>"). lock
// is false for read-only reconciliation. A non-nil outcome ends the executor.
func (s *Service) open(ctx context.Context, p coreprop.Proposal, where coreprop.Target, lock bool) (workdir, *outcome) {
	stop := func(o outcome) (workdir, *outcome) { return workdir{}, &o }
	switch w := where.(type) {
	case coreprop.InWorktree:
		wt, err := s.cfg.Resolver.Worktree(ctx, w.ID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return stop(failed(coreprop.FailPrecondition, false, "The worktree for this change no longer exists."))
			}
			return stop(failed(coreprop.FailExec, true, "Yggdrasil could not look up the worktree. Try again."))
		}
		if wt.Repo != p.Repo {
			return stop(failed(coreprop.FailPrecondition, false, "The worktree for this change belongs to another repo."))
		}
		if !s.repoTracked(ctx, wt.Repo) {
			return stop(failed(coreprop.FailRepoUntracked, false, "This repo is no longer tracked, so nothing was run. Track the repo again, then ask for a new proposal."))
		}
		return workdir{dir: wt.Path, repo: wt.Repo}, nil
	case coreprop.MainCheckout:
		info, err := s.cfg.Resolver.Repo(ctx, w.Repo)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return stop(failed(coreprop.FailExec, true, "Yggdrasil could not look up the repo. Try again."))
		}
		if err != nil || !info.Tracked {
			return stop(failed(coreprop.FailRepoUntracked, false, "This repo is no longer tracked, so nothing was run. Track the repo again, then ask for a new proposal."))
		}
		wd := workdir{dir: info.Path, repo: w.Repo, main: true}
		if lock {
			release, holder, ok := s.cfg.Lock.TryAcquire([]core.RepoID{w.Repo}, mainlock.Owner{Kind: mainlock.KindGitAction, ID: "proposal:" + string(p.ID)})
			if !ok {
				return stop(failed(coreprop.FailPrecondition, true, "The main checkout of this repo is in use by %s. Retry when it has finished.", holderText(holder)))
			}
			wd.release = release
		}
		return wd, nil
	}
	return stop(failed(coreprop.FailPrecondition, false, "This change does not say where to run."))
}

func holderText(h mainlock.Holder) string {
	switch h.Owner.Kind {
	case mainlock.KindBuild:
		return "a build"
	case mainlock.KindTask:
		return "an agent task"
	case mainlock.KindGitAction:
		return "another git action"
	}
	return "another job"
}

// gitFailure turns an error from the git wrapper into a failed outcome.
func gitFailure(err error) outcome {
	var pe *git.PushError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failed(coreprop.FailInterrupted, true, "The operation took too long and was stopped. It may or may not have finished: check the repo, then retry.")
	case errors.Is(err, context.Canceled):
		return failed(coreprop.FailInterrupted, true, "The operation was stopped before it finished. Check the repo, then retry.")
	case errors.Is(err, git.ErrStaleHead):
		return failed(coreprop.FailStale, false, "The branch has new commits since this was proposed. Ask the agent for a new proposal.")
	case errors.Is(err, git.ErrNothingToCommit):
		return failed(coreprop.FailExec, false, "%s", git.UserMessage(err))
	case errors.Is(err, git.ErrNoAuthor):
		return failed(coreprop.FailExec, true, "%s", git.UserMessage(err))
	case errors.Is(err, git.ErrDefaultBranch):
		return failed(coreprop.FailPrecondition, false, "%s", git.UserMessage(err))
	case errors.Is(err, git.ErrUnsafeRemote):
		return failed(coreprop.FailUnsafeRemote, false, "%s", git.UserMessage(err))
	case errors.As(err, &pe):
		msg := pe.UserMessage()
		if pe.Reason != "" {
			msg += " The remote said: " + pe.Reason
		}
		if pe.Kind == git.PushNetwork {
			return failed(coreprop.FailExec, true, "%s", msg)
		}
		return failed(coreprop.FailRemote, false, "%s", msg)
	}
	return failed(coreprop.FailExec, true, "%s", git.UserMessage(err))
}

func pathStrings(ps []coreprop.RelPath) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// checkRemoteURL applies the proposals URL rules to a remote's URL and returns
// its host. A plain local folder (a bare repo on this machine) has no host and
// no credentials; every other form must pass coreprop.CheckRemote.
func checkRemoteURL(u string) (host string, err error) {
	if git.IsLocalPath(u) {
		return "", nil
	}
	r, err := coreprop.CheckRemote(u)
	if err != nil {
		return "", err
	}
	return r.Host, nil
}

var errUnsafeRemote = failed(coreprop.FailUnsafeRemote, false,
	"The remote address has a password or token in it, or is not a normal address, so nothing was sent. Remove the credentials from the remote URL (use SSH keys or a credential manager), then ask for a new proposal.")

// errOutcome describes why a precondition check could not even run.
func readFailure(what string, err error) outcome {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return gitFailure(err)
	}
	return failed(coreprop.FailExec, true, "Yggdrasil could not %s: %s", what, strings.TrimSpace(git.UserMessage(err)))
}
