package tasks

import (
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// LockProber is an optional extension of MainLock: Held says, without taking
// anything, who holds the lock of the first of repos. The scheduler uses it to
// keep a task that edits a main checkout waiting, instead of admitting it just
// to have TryAcquire refuse it (which would flap the status and write an audit
// entry on every retry). A MainLock without it still works through the
// LocksDenied path.
type LockProber interface {
	Held(repos []core.RepoID) (holder string, held bool)
}

// mainBlocker reports whether the main checkout a current_branch task is about
// to edit is held by someone else. The answer is a hint: TryAcquire stays the
// authority and a lost race is still handled by LocksDenied.
func (s *Service) mainBlocker(id core.TaskID) (holder string, blocked bool) {
	p, ok := s.c.MainLock.(LockProber)
	if !ok {
		return "", false
	}
	st, err := s.snapshot(s.bgCtx(), id)
	if err != nil || st.Task.Mode != core.ModeCurrentBranch || len(st.Row.RepoIDs) == 0 {
		return "", false
	}
	holder, held := p.Held(st.Row.RepoIDs[:1])
	if !held || holder == "task:"+string(id) {
		return "", false
	}
	return holder, true
}

func (s *Service) setBlocked(id core.TaskID, holder string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if holder == "" {
		delete(s.blocked, id)
		return
	}
	s.blocked[id] = holder
}

func (s *Service) blockedReason(id core.TaskID) string {
	s.mu.Lock()
	holder, ok := s.blocked[id]
	s.mu.Unlock()
	if !ok {
		return ""
	}
	kind, _, _ := strings.Cut(holder, ":")
	if kind == "" {
		kind = "another action"
	}
	return "Waiting: main checkout held by " + kind
}
