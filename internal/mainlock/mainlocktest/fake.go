// Package mainlocktest holds a scripted fake MainLock and the contract suite
// that both the fake and the real Registry must pass.
package mainlocktest

import (
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

// Fake behaves like a real lock, plus scripted denials: Deny makes acquires
// touching repo fail with the given holder until Allow is called.
type Fake struct {
	mu     sync.Mutex
	real   mainlock.Registry
	denies map[core.RepoID]mainlock.Owner
}

var _ mainlock.MainLock = (*Fake)(nil)

// Deny scripts a denial on repo, held by by.
func (f *Fake) Deny(repo core.RepoID, by mainlock.Owner) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denies == nil {
		f.denies = map[core.RepoID]mainlock.Owner{}
	}
	f.denies[repo] = by
}

// Allow removes a scripted denial.
func (f *Fake) Allow(repo core.RepoID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.denies, repo)
}

// TryAcquire applies scripted denials, then behaves like the real registry.
func (f *Fake) TryAcquire(repos []core.RepoID, owner mainlock.Owner) (func(), mainlock.Holder, bool) {
	f.mu.Lock()
	for _, r := range repos {
		if by, ok := f.denies[r]; ok && by != owner {
			f.mu.Unlock()
			return func() {}, mainlock.Holder{Repo: r, Owner: by}, false
		}
	}
	f.mu.Unlock()
	return f.real.TryAcquire(repos, owner)
}
