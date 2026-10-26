package builds

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// maxRunning is the number of builds that run at once (BL-03).
const maxRunning = 1

// worker admits queued builds. It runs until ctx is done and re-runs Next each
// time it is woken, so a finished build is followed by the next one (S3).
func (s *Service) worker(ctx context.Context) {
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retry:
		}
		retry = nil
		if s.admitAll(ctx) {
			// A task, a git action or another build holds a checkout: the
			// build stays queued and the queue tries again soon.
			d := s.d.LockRetry
			if d <= 0 {
				d = 500 * time.Millisecond
			}
			retry = time.After(d)
		}
	}
}

// admitAll admits queued builds while there is room. It reports true when the
// next build could not start because its main checkouts are held.
func (s *Service) admitAll(ctx context.Context) (blocked bool) {
	for ctx.Err() == nil {
		refs, err := s.d.Store.ListQueued(ctx)
		if err != nil {
			s.d.Log.Error("builds: list queued", "err", err)
			return
		}
		running, err := s.d.Store.CountRunning(ctx)
		if err != nil {
			s.d.Log.Error("builds: count running", "err", err)
			return false
		}
		id, ok := corebuilds.Next(refs, running, maxRunning)
		if !ok {
			return false
		}
		if !s.lockFor(ctx, id) {
			return true
		}
		res, err := s.send(ctx, id, corebuilds.Admit{Now: s.d.Clock()})
		if err != nil || !res.changed {
			s.unlock(id)
			return false // an Admit that changes nothing would loop forever
		}
	}
	return false
}

// lockFor takes the main-checkout lock of every repo of a main-checkout build,
// all or nothing, before its first step starts. It is true when the build may
// start. Worktree builds, a missing lock or an unreadable build need no lock.
func (s *Service) lockFor(ctx context.Context, id core.BuildID) bool {
	if s.d.MainLock == nil {
		return true
	}
	st, _, err := s.d.Store.Get(ctx, id)
	if err != nil {
		return true // the Admit that follows reports the real problem
	}
	spec, ok := specOf(st)
	if !ok {
		return true
	}
	if _, main := spec.Target.(corebuilds.MainCheckout); !main {
		return true
	}
	release, holder, ok := s.d.MainLock.TryAcquire(uniqueRepos(spec), mainlock.Owner{Kind: mainlock.KindBuild, ID: string(id)})
	if !ok {
		s.d.Log.Info("builds: build waits for a main checkout", "build", id, "repo", holder.Repo, "holder", string(holder.Owner.Kind))
		return false
	}
	s.mu.Lock()
	s.locks[id] = release
	s.mu.Unlock()
	return true
}

// unlock gives the checkouts of a build back. It is safe to call twice.
func (s *Service) unlock(id core.BuildID) {
	s.mu.Lock()
	release := s.locks[id]
	delete(s.locks, id)
	s.mu.Unlock()
	if release != nil {
		release()
	}
}
