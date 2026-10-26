// Package mainlock is the shared main-checkout lock: a build, a git action
// and a current-branch task must not touch the same repo's main checkout at
// the same time. It is a shell capability (a mutex-guarded registry), not core.
package mainlock

import (
	"sort"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// OwnerKind says what kind of work holds a lock.
type OwnerKind string

const (
	KindTask      OwnerKind = "task"
	KindBuild     OwnerKind = "build"
	KindGitAction OwnerKind = "git_action"
)

// Owner identifies the holder of a lock. Two owners are the same when both
// fields match.
type Owner struct {
	Kind OwnerKind
	ID   string
}

// Holder describes who holds a repo's main checkout, for UIs.
type Holder struct {
	Repo  core.RepoID
	Owner Owner
}

// MainLock is the consumer-side interface. On denial, release is a no-op,
// holder names the first conflicting repo's holder and ok is false.
type MainLock interface {
	TryAcquire(repos []core.RepoID, owner Owner) (release func(), holder Holder, ok bool)
}

// Registry is the real implementation. The zero value is ready to use.
type Registry struct {
	mu   sync.Mutex
	held map[core.RepoID]Owner
}

var _ MainLock = (*Registry)(nil)

func noop() {}

// TryAcquire takes every repo or none. The same owner may re-acquire.
func (r *Registry) TryAcquire(repos []core.RepoID, owner Owner) (func(), Holder, bool) {
	ids := sortedUnique(repos)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if cur, taken := r.held[id]; taken && cur != owner {
			return noop, Holder{Repo: id, Owner: cur}, false
		}
	}
	if r.held == nil {
		r.held = make(map[core.RepoID]Owner)
	}
	for _, id := range ids {
		r.held[id] = owner
	}
	return func() { r.release(ids, owner) }, Holder{}, true
}

func (r *Registry) release(ids []core.RepoID, owner Owner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if r.held[id] == owner {
			delete(r.held, id)
		}
	}
}

// Holder reports who holds repo, if anyone.
func (r *Registry) Holder(repo core.RepoID) (Holder, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.held[repo]
	if !ok {
		return Holder{}, false
	}
	return Holder{Repo: repo, Owner: o}, true
}

func sortedUnique(repos []core.RepoID) []core.RepoID {
	out := append([]core.RepoID(nil), repos...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for i, id := range out {
		if i == 0 || id != out[n-1] {
			out[n] = id
			n++
		}
	}
	return out[:n]
}
