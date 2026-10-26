package main

import (
	"sort"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/mainlock/mainlocktest"
)

// taskLockAsMainLock lets the shared contract suite drive the string-owner
// taskLock adapter through the mainlock.MainLock interface.
type taskLockAsMainLock struct{ l taskLock }

func (a taskLockAsMainLock) TryAcquire(repos []core.RepoID, owner mainlock.Owner) (func(), mainlock.Holder, bool) {
	rel, _, ok := a.l.TryAcquire(repos, string(owner.Kind)+":"+owner.ID)
	if ok {
		return rel, mainlock.Holder{}, true
	}
	sorted := append([]core.RepoID(nil), repos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, r := range sorted {
		if h, held := a.l.reg.Holder(r); held && h.Owner != owner {
			return func() {}, h, false
		}
	}
	return func() {}, mainlock.Holder{}, false
}

func TestTaskLockPassesTheMainLockContract(t *testing.T) {
	mainlocktest.MainLockContract(t, func() mainlock.MainLock {
		return taskLockAsMainLock{l: taskLock{reg: &mainlock.Registry{}}}
	})
}

func TestFakeMainLockPassesTheMainLockContract(t *testing.T) {
	mainlocktest.MainLockContract(t, func() mainlock.MainLock { return &mainlocktest.Fake{} })
}
