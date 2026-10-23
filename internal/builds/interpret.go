package builds

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// interpret performs the effect commands of a committed step. It runs on the
// owner goroutine, after the commit. Inputs it produces itself (a step that
// could not start) are returned and handled next, in order.
func (s *Service) interpret(ctx context.Context, id core.BuildID, before, after corebuilds.State, cmds []corebuilds.Command) []msg {
	var follow []msg
	for _, c := range cmds {
		switch c := c.(type) {
		case corebuilds.Publish:
			if s.d.Publisher != nil {
				s.d.Publisher.PublishBuildChanged(c.ID, corebuilds.Status(after))
			}
		case corebuilds.Wake:
			s.signalWake()
		case corebuilds.StartStep:
			follow = append(follow, s.startStep(ctx, c)...)
		case corebuilds.KillStep:
			s.kill(stepKey{c.BuildID, c.Index, c.Attempt})
		case corebuilds.CollectArtifacts:
			s.collect(c)
		}
	}
	s.lifecycle(ctx, id, before, after)
	return follow
}

// lifecycle tells subscribers that a build started and the status service that
// repos may have changed on disk.
func (s *Service) lifecycle(ctx context.Context, id core.BuildID, before, after corebuilds.State) {
	_, wasQueued := before.(corebuilds.Queued)
	if run, ok := after.(corebuilds.Running); ok && wasQueued {
		ev := BuildStarted{ID: id, Target: run.Spec.Target, Repos: uniqueRepos(run.Spec)}
		for _, fn := range s.subs {
			fn(ev)
		}
	}
	fin, ok := after.(corebuilds.Finished)
	if !ok {
		return
	}
	if _, was := before.(corebuilds.Finished); was || s.d.Status == nil {
		return
	}
	s.pruneStarted(id)
	repos := uniqueRepos(fin.Spec)
	refresh := func(rctx context.Context) {
		if err := s.d.Status.Refresh(rctx, repos, corerepos.ReasonBuild); err != nil {
			s.d.Log.Warn("refresh repo status after a build", "build", id, "err", err)
		}
	}
	// Off the owner goroutine: a slow status refresh must not stall every build.
	s.mu.Lock()
	runCtx, g := s.ctx, s.g
	s.mu.Unlock()
	if g == nil {
		refresh(ctx)
		return
	}
	g.Go(func() error { refresh(runCtx); return nil })
}

// pruneStarted forgets the start records of a finished build: a Finished build
// never emits StartStep again, so the S1 guarantee holds without them.
func (s *Service) pruneStarted(id core.BuildID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.started {
		if k.build == id {
			delete(s.started, k)
		}
	}
}

func (s *Service) kill(k stepKey) {
	s.mu.Lock()
	cancel := s.live[k]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// startFailed writes the reason into the step log, so every started step has
// a log (S5), and returns the input that ends the step.
func (s *Service) startFailed(c corebuilds.StartStep, lw LogWriter, reason corebuilds.StartFailure, text string, cause error) []msg {
	if s.stopping() {
		// Shutting down: the build stays Running and is interrupted on restart.
		if lw != nil {
			_ = lw.Close()
		}
		return nil
	}
	s.d.Log.Warn("build step did not start", "build", c.BuildID, "step", c.Index, "reason", string(reason), "err", cause)
	if lw != nil {
		_, _ = fmt.Fprintln(lw, text)
		_ = lw.Close()
	}
	return []msg{{id: c.BuildID, in: corebuilds.StepStartFailed{Index: c.Index, Attempt: c.Attempt, Reason: reason, Now: s.d.Clock()}}}
}

// startStep starts the process of a step. It is the only non-idempotent
// effect: a second start for the same (build, index, attempt) is refused (S1).
func (s *Service) startStep(ctx context.Context, c corebuilds.StartStep) []msg {
	key := stepKey{c.BuildID, c.Index, c.Attempt}
	s.mu.Lock()
	_, dup := s.started[key]
	s.started[key] = struct{}{}
	runCtx, g := s.ctx, s.g
	s.mu.Unlock()
	if dup {
		s.d.Log.Error("builds: refused a second start of a step", "build", c.BuildID, "step", c.Index, "attempt", c.Attempt)
		return nil
	}
	if len(c.Argv) == 0 {
		return s.startFailed(c, nil, corebuilds.StartBadArgs, "", errors.New("empty argv"))
	}
	lw, err := s.d.Logs.Open(c.BuildID, c.Index, c.Attempt)
	if err != nil {
		return s.startFailed(c, nil, corebuilds.StartOther, "", err)
	}
	dir, err := s.stepDir(ctx, c)
	if err != nil {
		return s.startFailed(c, lw, corebuilds.StartOther, "Could not find the folder to build in. Check that the repo is still tracked.", err)
	}
	if !s.d.DirExists(dir) {
		return s.startFailed(c, lw, corebuilds.StartDirMissing,
			"The folder to build in no longer exists. Restore it or pick another worktree.", errors.New("directory missing"))
	}
	env, err := s.d.Env.Build(safety.EnvOptions{Extra: map[string]string{"CI": "true"}})
	if err != nil {
		return s.startFailed(c, lw, corebuilds.StartOther, "Could not prepare the build environment.", err)
	}
	exe, err := s.d.Resolve(c.Argv[0], dir, env)
	if err != nil {
		reason, text := classifyStart(err, c.Argv[0])
		return s.startFailed(c, lw, reason, text, err)
	}
	argv := append([]string{exe}, c.Argv[1:]...)
	stepCtx, cancel := context.WithCancel(runCtx)
	p, err := s.d.Start(stepCtx, platform.Spec{Argv: argv, Dir: dir, Env: env, Stdout: lw, Stderr: lw})
	if err != nil {
		cancel()
		reason, text := classifyStart(err, c.Argv[0])
		return s.startFailed(c, lw, reason, text, err)
	}
	s.mu.Lock()
	s.live[key] = cancel
	s.mu.Unlock()
	g.Go(func() error {
		code, werr := p.Wait()
		truncated := p.Truncated()
		_ = lw.Close()
		cancel()
		s.mu.Lock()
		delete(s.live, key)
		s.mu.Unlock()
		if runCtx.Err() != nil {
			// ygg is shutting down: leave the build Running, so the next start
			// records it as interrupted rather than as a failed step.
			return nil //nolint:nilerr // shutting down: the build is left Running on purpose
		}
		if werr != nil && code == 0 {
			code = -1
		}
		s.post(c.BuildID, corebuilds.StepExited{Index: c.Index, Attempt: c.Attempt, ExitCode: code, LogTruncated: truncated, Now: s.d.Clock()})
		return nil
	})
	return nil
}

func (s *Service) stepDir(ctx context.Context, c corebuilds.StartStep) (string, error) {
	switch t := c.Target.(type) {
	case corebuilds.MainCheckout:
		return s.d.Locator.RepoDir(ctx, c.RepoID)
	case corebuilds.InWorktree:
		return s.d.Locator.WorktreeDir(ctx, t.WorktreeID)
	}
	return "", errors.New("build has no target")
}

// classifyStart maps a start error to the failure the core knows and a plain
// message for the step log.
func classifyStart(err error, name string) (corebuilds.StartFailure, string) {
	switch {
	case errors.Is(err, platform.ErrNotFound):
		return corebuilds.StartNotFound, fmt.Sprintf("Could not start %q: it is not installed or not on the PATH. Install it, or set the command in the repo's build settings.", name)
	case errors.Is(err, platform.ErrBatchArg):
		return corebuilds.StartBadArgs, fmt.Sprintf("Could not start %q: an argument contains a character a Windows batch file cannot take safely. Remove it or split the step.", name)
	case errors.Is(err, platform.ErrDirMissing):
		return corebuilds.StartDirMissing, "The folder to build in no longer exists. Restore it or pick another worktree."
	}
	return corebuilds.StartOther, fmt.Sprintf("Could not start %q. See the Yggdrasil log for details.", name)
}

// collect looks for artifacts in the background (read only) and feeds the
// result back as an input.
func (s *Service) collect(c corebuilds.CollectArtifacts) {
	s.mu.Lock()
	runCtx, g := s.ctx, s.g
	s.mu.Unlock()
	g.Go(func() error {
		arts := []corebuilds.Artifact{}
		for _, repo := range c.RepoIDs {
			dir, err := s.stepDir(runCtx, corebuilds.StartStep{RepoID: repo, Target: c.Target})
			if err != nil {
				s.d.Log.Warn("find artifacts: repo folder unknown", "build", c.BuildID, "repo", repo, "err", err)
				continue
			}
			found, err := s.d.Artifacts.Find(s.d.OpenFS(dir), repo)
			if err != nil {
				s.d.Log.Warn("find artifacts", "build", c.BuildID, "repo", repo, "err", err)
			}
			arts = append(arts, found...)
		}
		s.post(c.BuildID, corebuilds.ArtifactsCollected{Artifacts: arts, Now: s.d.Clock()})
		return nil
	})
}

// stopping reports whether the service context is done.
func (s *Service) stopping() bool {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	return ctx != nil && ctx.Err() != nil
}
