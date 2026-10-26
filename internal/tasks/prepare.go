package tasks

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

func (s *Service) startPrepare(id core.TaskID, cmd coretasks.PrepareRun) {
	at := s.newAttempt(id, cmd.Turn, cmd.Attempt)
	if !s.spawn(s.guarded(id, "prepare", func() { s.prepare(id, at, cmd) })) {
		at.cancel()
	}
}

// prepare creates the worktrees and takes the main-checkout lock of a turn,
// then feeds the result back to the core: Prepared, PrepareFailed or
// LocksDenied. Nothing here retries Ensure or Record: a failure is the turn's
// failure, and the worktrees that exist stay recorded.
func (s *Service) prepare(id core.TaskID, at *attempt, cmd coretasks.PrepareRun) {
	in := s.safePrepare(id, at, cmd)
	res, ok := s.feed(id, in, applyOpts{})
	if !ok {
		s.abandon(id, "prepare")
		return
	}
	// A kill that raced the preparation: if the core did not start the run,
	// nothing will ever report the end of this attempt except us. The input is
	// ignored when the core already finished the turn (the stop phase is only
	// in memory, so this also covers a task that was reloaded meanwhile).
	if at.ctx.Err() != nil && !hasStartRun(res.Cmds) {
		s.feed(id, coretasks.RunFinished{Turn: cmd.Turn, Attempt: cmd.Attempt, Status: coretasks.RunCancelled, Now: s.c.Now().UTC()}, applyOpts{})
	}
}

func hasStartRun(cmds []coretasks.Command) bool {
	for _, c := range cmds {
		if _, ok := c.(coretasks.StartRun); ok {
			return true
		}
	}
	return false
}

// safePrepare turns a panic of the preparation into PrepareFailed.
func (s *Service) safePrepare(id core.TaskID, at *attempt, cmd coretasks.PrepareRun) (in coretasks.Input) {
	defer func() {
		if v := recover(); v != nil {
			s.c.Log.Error("prepare panic", "task", id, "panic", v, "stack", string(debug.Stack()))
			in = coretasks.PrepareFailed{Turn: cmd.Turn, Attempt: cmd.Attempt, Reason: "Preparing the task failed unexpectedly.", Now: s.c.Now().UTC()}
		}
	}()
	return s.doPrepare(id, at, cmd)
}

func (s *Service) doPrepare(id core.TaskID, at *attempt, cmd coretasks.PrepareRun) coretasks.Input {
	ctx := at.ctx
	failed := func(reason string) coretasks.Input {
		return coretasks.PrepareFailed{Turn: cmd.Turn, Attempt: cmd.Attempt, Reason: reason, Now: s.c.Now().UTC()}
	}
	if ctx.Err() != nil {
		return failed("The task was cancelled before it started.")
	}
	st, err := s.snapshot(s.bgCtx(), id)
	if err != nil {
		s.c.Log.Error("prepare: load task", "task", id, "err", err)
		return failed("The task could not be read. Try again.")
	}
	repos := make([]core.Repo, 0, len(st.Row.RepoIDs))
	for _, rid := range st.Row.RepoIDs {
		r, err := s.c.Store.GetRepo(ctx, rid)
		if err != nil {
			return failed("A repo of this task isn't tracked any more. Track it again or start a new task.")
		}
		repos = append(repos, r)
	}
	if len(repos) == 0 {
		return failed("This task has no repo.")
	}
	var paths []string
	switch st.Task.Mode {
	case core.ModeAsk:
		// Ask reads the task's worktrees when it has some (an earlier Code
		// turn), else the main checkouts. It creates nothing.
		wts, err := s.c.Store.ListWorktrees(ctx, id)
		if err != nil {
			return failed("The task's worktrees could not be read. Try again.")
		}
		byRepo := map[core.RepoID]string{}
		for _, w := range wts {
			if w.Status == core.WorktreeActive {
				byRepo[w.RepoID] = w.Path
			}
		}
		for _, r := range repos {
			if p, ok := byRepo[r.ID]; ok {
				paths = append(paths, p)
			} else {
				paths = append(paths, r.Path)
			}
		}
	case core.ModeCurrentBranch:
		rel, holder, ok := s.c.MainLock.TryAcquire([]core.RepoID{repos[0].ID}, "task:"+string(id))
		if !ok {
			return coretasks.LocksDenied{Turn: cmd.Turn, Attempt: cmd.Attempt, Repo: repos[0].ID, Holder: holder}
		}
		s.mu.Lock()
		s.releases[id] = rel
		s.mu.Unlock()
		paths = []string{repos[0].Path}
	case core.ModeCode, core.ModeProject:
		use := repos
		if st.Task.Mode == core.ModeCode {
			use = repos[:1]
		}
		refs := make([]worktrees.RepoRef, len(use))
		for i, r := range use {
			refs[i] = worktrees.RepoRef{ID: r.ID, Path: r.Path, DefaultBranch: r.DefaultBranch}
		}
		// Ensure takes no lock of its own: one call per task at a time.
		mu := s.prepMutex(id)
		mu.Lock()
		wts, err := s.c.Worktrees.Ensure(ctx, id, refs)
		mu.Unlock()
		if err != nil {
			s.c.Log.Warn("ensure worktrees", "task", id, "err", err)
			if ctx.Err() != nil {
				return failed("The task was cancelled before it started.")
			}
			return failed(userMessage(err, "The worktree could not be created. Check the repo and try again."))
		}
		for _, r := range use {
			found := false
			for _, w := range wts {
				if w.RepoID == r.ID {
					paths = append(paths, w.Path)
					found = true
					break
				}
			}
			if !found {
				return failed("The worktree could not be created. Check the repo and try again.")
			}
		}
	default:
		return failed("That mode isn't available.")
	}
	dirs := make([]string, len(paths))
	for i, p := range paths {
		d, err := platform.LongPath(p)
		if err != nil {
			var um interface{ UserMessage() string }
			if errors.As(err, &um) {
				return failed(um.UserMessage())
			}
			return failed(fmt.Sprintf("The folder of repo %s can't be used.", repos[min(i, len(repos)-1)].Name))
		}
		dirs[i] = d
	}
	return coretasks.Prepared{Turn: cmd.Turn, Attempt: cmd.Attempt, Dir: dirs[0], ExtraDirs: dirs[1:], Now: s.c.Now().UTC()}
}
