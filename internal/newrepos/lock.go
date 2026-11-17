package newrepos

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

// TryLock adapts the non-blocking mainlock registry to the blocking MainLock
// port: it polls TryAcquire until the lock is free or ctx ends. The lock is
// held by one stage while it mutates the checkout and released before the
// stage's result is recorded, so it is never held while waiting for another
// lock-taking service.
func TryLock(m mainlock.MainLock, poll time.Duration) MainLock {
	if poll <= 0 {
		poll = 25 * time.Millisecond
	}
	return tryLock{m: m, poll: poll}
}

type tryLock struct {
	m    mainlock.MainLock
	poll time.Duration
}

func (l tryLock) Lock(ctx context.Context, repo core.RepoID) (func(), error) {
	owner := mainlock.Owner{Kind: mainlock.KindGitAction, ID: "newrepo:" + string(repo)}
	for {
		if release, _, ok := l.m.TryAcquire([]core.RepoID{repo}, owner); ok {
			return release, nil
		}
		t := time.NewTimer(l.poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
