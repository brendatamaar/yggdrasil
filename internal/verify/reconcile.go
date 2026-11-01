package verify

import (
	"context"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// turnSeq is the turn sequence number a state is for (0 for None).
func turnSeq(s cv.State) int {
	switch s := s.(type) {
	case cv.Skipped:
		return s.TurnSeq
	case cv.Unverifiable:
		return s.TurnSeq
	case cv.Bootstrapping:
		return s.TurnSeq
	case cv.Verifying:
		return s.TurnSeq
	case cv.Passed:
		return s.TurnSeq
	case cv.Failed:
		return s.TurnSeq
	case cv.Interrupted:
		return s.TurnSeq
	case cv.Cancelled:
		return s.TurnSeq
	}
	return 0
}

// liveOf returns the build a live state waits for and the length of the chain
// it was queued with.
func liveOf(s cv.State) (build core.BuildID, chainLen int, live bool) {
	switch s := s.(type) {
	case cv.Bootstrapping:
		return s.Build, len(s.Plan.Bootstrap), true
	case cv.Verifying:
		return s.Build, len(s.Plan.Verify), true
	}
	return "", 0, false
}

// Reconcile is one pass over the stored state: it starts verification for
// every task that went idle in review with a turn the aggregate has not seen,
// feeds the outcome of builds that finished (the watchdog for results the
// subscription never delivered), re-queues builds that are missing, and resumes
// interrupted runs once. Errors of one aggregate do not stop the others.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns, err := s.d.Store.ReviewTurns(ctx)
	if err != nil {
		return fmt.Errorf("list tasks in review: %w", err)
	}
	inReview := make(map[core.TaskID]int, len(turns))
	for _, t := range turns {
		turn, seq, perr := ParseReviewTurn(t)
		if perr != nil {
			s.d.Log.Warn("skipping a turn that is not valid", "task", t.Task, "err", perr)
			continue
		}
		inReview[t.Task] = seq
		s.triggerTask(ctx, t.Task, turn, seq)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	s.pollLive(ctx)
	s.resumeInterrupted(ctx, inReview)
	return ctx.Err()
}

// safely runs f and logs a panic, leaving the aggregate as it is stored.
func (s *Service) safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("verification step panicked", "where", what, "panic", fmt.Sprint(r))
		}
	}()
	f()
}

// triggerTask starts TurnEnded for each active worktree of a task whose
// aggregate has not seen the turn.
func (s *Service) triggerTask(ctx context.Context, task core.TaskID, turn cv.TurnID, seq int) {
	wts, err := s.d.Store.Worktrees(ctx, task)
	if err != nil {
		s.d.Log.Warn("could not list worktrees", "task", task, "err", err)
		return
	}
	for _, wt := range wts {
		k := key{task: task, repo: wt.RepoID, wt: wt.ID}
		s.safely("turn ended "+string(task), func() {
			rec, lerr := s.load(ctx, k)
			if lerr != nil {
				s.d.Log.Warn("could not load a verification", "task", task, "err", lerr)
				return
			}
			if turnSeq(rec.State) >= seq {
				return
			}
			_, _, rerr := s.run(ctx, k, systemActor, func(r Record) (cv.Input, string, bool, error) {
				if turnSeq(r.State) >= seq {
					return nil, "", false, nil
				}
				facts, plan, note, ferr := s.readInputs(ctx, r)
				if ferr != nil {
					return nil, "", false, ferr
				}
				return cv.TurnEnded{
					Turn: turn, Seq: seq, Facts: facts, Plan: plan,
					BootBuild: core.BuildID(s.d.IDs("bld")), VerifyBuild: core.BuildID(s.d.IDs("bld")), Now: s.now(),
				}, note, true, nil
			})
			s.noteFactResult(k, rerr)
		})
	}
}

// noteFactResult counts consecutive failures of one aggregate and logs only a
// persistent one (nothing is stored; the next pass retries).
func (s *Service) noteFactResult(k key, err error) {
	if err == nil {
		delete(s.factFailures, k)
		return
	}
	s.factFailures[k]++
	if n := s.factFailures[k]; n == failureLogWatch {
		s.d.Log.Warn("verification could not start: reading the repo keeps failing", "task", k.task, "repo", k.repo, "err", err)
	}
}

// pollLive looks at every live aggregate's build.
func (s *Service) pollLive(ctx context.Context) {
	rows, err := s.d.Store.ListLive(ctx)
	if err != nil {
		s.d.Log.Warn("could not list running verifications", "err", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		rec, perr := VerificationFromRow(row)
		if perr != nil {
			// A row the shell cannot read cannot be driven; say so loudly.
			s.d.Log.Error("a running verification is stored in a form that cannot be read", "verification", row.ID, "err", perr)
			continue
		}
		s.safely("poll "+row.ID, func() { s.pollOne(ctx, rec) })
	}
}

func (s *Service) pollOne(ctx context.Context, rec Record) {
	build, chainLen, live := liveOf(rec.State)
	if !live {
		return
	}
	st, err := s.d.Builds.Get(ctx, build)
	switch {
	case err == nil:
	case isNotFound(err):
		// The enqueue never happened (a crash after the commit): queue the same
		// ID again. The builds service returns the existing build if it exists.
		s.d.Log.Info("re-queueing a verification build", "verification", rec.ID, "build", build)
		s.interpret(ctx, rec, []cv.Command{s.enqueueCommand(rec)})
		return
	default:
		s.d.Log.Warn("could not read a verification build", "verification", rec.ID, "build", build, "err", err)
		return
	}
	res, finished := ToBuildResult(chainLen, st)
	if !finished {
		return
	}
	s.feedResult(ctx, keyOf(rec), build, res)
}

// enqueueCommand rebuilds the EnqueueBuild of a live state.
func (s *Service) enqueueCommand(rec Record) cv.EnqueueBuild {
	switch st := rec.State.(type) {
	case cv.Bootstrapping:
		return cv.EnqueueBuild{Build: st.Build, Stage: cv.StageBootstrap, Spec: cv.BuildSpec{Worktree: rec.Worktree, Steps: st.Plan.Bootstrap}}
	case cv.Verifying:
		return cv.EnqueueBuild{Build: st.Build, Stage: cv.StageVerify, Spec: cv.BuildSpec{Worktree: rec.Worktree, Steps: st.Plan.Verify}}
	}
	return cv.EnqueueBuild{}
}

// feedResult delivers a finished build's result. The core ignores a result it
// cannot use, which would leave the aggregate waiting for ever: if the
// aggregate still waits for that build afterwards, the build is reported as
// interrupted, which the core always accepts, and the user can verify again.
func (s *Service) feedResult(ctx context.Context, k key, build core.BuildID, res cv.BuildResult) {
	after, changed, err := s.run(ctx, k, systemActor, func(Record) (cv.Input, string, bool, error) {
		return cv.BuildFinished{Build: build, Result: res, Now: s.now()}, "", true, nil
	})
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.Warn("could not record a verification result", "task", k.task, "repo", k.repo, "err", err)
		}
		return
	}
	if changed {
		return
	}
	if b, _, live := liveOf(after.State); !live || b != build {
		return
	}
	s.d.Log.Warn("a build result was refused; marking the verification interrupted", "verification", after.ID, "build", build)
	_, changed, err = s.run(ctx, k, systemActor, func(Record) (cv.Input, string, bool, error) {
		return cv.BuildFinished{Build: build, Result: cv.BuildInterrupted{}, Now: s.now()}, "", true, nil
	})
	if err != nil || !changed {
		s.d.Log.Error("a verification is stuck and could not be marked interrupted", "verification", after.ID, "build", build, "err", err)
	}
}

// resumeInterrupted re-verifies interrupted runs (ygg stopped) whose task is
// still idle in review at the same turn. Step allows it once (MaxAutoAttempts);
// a second pass finds the state already live and changes nothing.
func (s *Service) resumeInterrupted(ctx context.Context, inReview map[core.TaskID]int) {
	rows, err := s.d.Store.ListInterrupted(ctx)
	if err != nil {
		s.d.Log.Warn("could not list interrupted verifications", "err", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		rec, perr := VerificationFromRow(row)
		if perr != nil {
			s.d.Log.Warn("skipping an interrupted verification that cannot be read", "verification", row.ID, "err", perr)
			continue
		}
		in, ok := rec.State.(cv.Interrupted)
		if !ok || in.Attempt >= cv.MaxAutoAttempts || inReview[rec.Task] != in.TurnSeq {
			continue
		}
		k := keyOf(rec)
		s.safely("resume "+row.ID, func() {
			_, _, rerr := s.run(ctx, k, systemActor, func(r Record) (cv.Input, string, bool, error) {
				if _, still := r.State.(cv.Interrupted); !still {
					return nil, "", false, nil
				}
				facts, plan, note, ferr := s.readInputs(ctx, r)
				if ferr != nil {
					return nil, "", false, ferr
				}
				return cv.Resume{
					Facts: facts, Plan: plan, BootBuild: core.BuildID(s.d.IDs("bld")), VerifyBuild: core.BuildID(s.d.IDs("bld")), Now: s.now(),
				}, note, true, nil
			})
			s.noteFactResult(k, rerr)
		})
	}
}
