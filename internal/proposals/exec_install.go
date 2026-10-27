package proposals

import (
	"context"
	"errors"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// execInstall enqueues the single install step through the builds service and
// waits for the build; the result is the build ID. The builds service takes the
// main-checkout lock itself (owner "build:<id>"), so this executor must not
// hold the lock too: the build would wait for it forever.
func (s *Service) execInstall(ctx context.Context, p coreprop.Proposal, a coreprop.Install) outcome {
	if !s.repoTracked(ctx, a.Repo) {
		return failed(coreprop.FailRepoUntracked, false, "This repo is no longer tracked, so nothing was run. Track the repo again, then ask for a new proposal.")
	}
	owner := "proposal:" + string(p.ID)
	var id core.BuildID
	found := false
	if bf, ok := s.cfg.Builds.(BuildFinder); ok {
		live, ok, err := bf.FindLive(ctx, owner)
		if err != nil {
			return readFailure("look for a running install", err)
		}
		id, found = live, ok
	}
	if !found {
		var err error
		id, err = s.cfg.Builds.Enqueue(ctx, a.Repo, a.Step, owner)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return gitFailure(err)
			}
			return failed(coreprop.FailExec, true, "The install could not be started. Check the repo's install command in its build settings, then retry.")
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.InstallTimeout)
	defer cancel()
	out, err := s.cfg.Builds.Wait(waitCtx, id)
	if err != nil {
		if waitCtx.Err() != nil && ctx.Err() == nil {
			return failed(coreprop.FailInterrupted, true, "The install (build %s) is taking very long, so Yggdrasil stopped waiting. It may still be running: look at its log, then retry if needed.", id)
		}
		return failed(coreprop.FailInterrupted, true, "Yggdrasil lost track of build %s. Look at its log, then retry if needed.", id)
	}
	if !out.Succeeded {
		reason := out.Reason
		if reason == "" {
			reason = "See its log."
		}
		return failed(coreprop.FailExec, true, "The install failed (build %s). %s", id, reason)
	}
	return done(coreprop.Result{BuildID: id, Note: "Install finished."})
}
