package repos_test

import (
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

func taskOwner(id string) mainlock.Owner { return mainlock.Owner{Kind: mainlock.KindTask, ID: id} }

func TestPullSkipsRepoHeldByATaskAndSaysWho(t *testing.T) {
	reg := &mainlock.Registry{}
	e := newActEnv(t, actOpts{repos: 2, lock: reg})
	_, _, ok := reg.TryAcquire([]core.RepoID{e.ids[0]}, taskOwner("tsk_1"))
	if !ok {
		t.Fatal("setup")
	}
	id, err := e.start("pull", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v := e.wait(id)
	if v.Items[0].State != "skipped" || v.Items[0].Reason != "build_running" || v.Items[1].State != "done" {
		t.Fatalf("items = %v", states(v))
	}
	if m := v.Items[0].Message; !strings.Contains(m, "task") {
		t.Errorf("message should name the holder kind: %q", m)
	}
	if got := e.git.called(); len(got) != 1 || got[0] != "pull:repo1" {
		t.Errorf("git calls = %v", got)
	}
	if h, ok := reg.Holder(e.ids[1]); ok {
		t.Errorf("the lock of a finished item stays held by %v", h)
	}
	if h, ok := reg.Holder(e.ids[0]); !ok || h.Owner != taskOwner("tsk_1") {
		t.Errorf("the task lost its lock: %v %v", h, ok)
	}
}

func TestFetchNeedsNoLock(t *testing.T) {
	reg := &mainlock.Registry{}
	e := newActEnv(t, actOpts{repos: 1, lock: reg})
	reg.TryAcquire([]core.RepoID{e.ids[0]}, taskOwner("tsk_1"))
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	if v := e.wait(id); v.Items[0].State != "done" {
		t.Fatalf("fetch = %v", states(v))
	}
}

func TestPullHoldsTheLockWhileItRuns(t *testing.T) {
	reg := &mainlock.Registry{}
	e := newActEnv(t, actOpts{repos: 1, lock: reg})
	e.git.gate = make(chan struct{})
	id, err := e.start("pull", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.git.started:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not start")
	}
	_, h, ok := reg.TryAcquire(e.ids, mainlock.Owner{Kind: mainlock.KindBuild, ID: "bld_1"})
	if ok {
		t.Fatal("a build got the checkout while a pull runs in it")
	}
	if h.Owner.Kind != mainlock.KindGitAction {
		t.Errorf("holder = %v", h)
	}
	close(e.git.gate)
	e.wait(id)
	rel, _, ok := reg.TryAcquire(e.ids, mainlock.Owner{Kind: mainlock.KindBuild, ID: "bld_1"})
	if !ok {
		t.Fatal("the lock was not released when the pull ended")
	}
	rel()
}

func TestCancelledPendingItemReleasesItsLock(t *testing.T) {
	reg := &mainlock.Registry{}
	e := newActEnv(t, actOpts{repos: 2, max: 1, lock: reg})
	e.git.gate = make(chan struct{})
	id, err := e.start("pull", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	<-e.git.started
	if _, err := e.svc.Cancel(id, "user"); err != nil {
		t.Fatal(err)
	}
	close(e.git.gate)
	e.wait(id)
	for _, r := range e.ids {
		if h, ok := reg.Holder(r); ok {
			t.Errorf("%s still held by %v", r, h)
		}
	}
}

// A build start racing a pull batch start: the two never hold the checkout at
// the same time.
func TestBuildStartAndPullBatchNeverOverlap(t *testing.T) {
	reg := &mainlock.Registry{}
	e := newActEnv(t, actOpts{repos: 1, lock: reg})
	repo := e.ids[0]
	dir := ""
	for i := 0; i < 40; i++ {
		done := make(chan struct{})
		var id corerepos.BatchID
		go func() {
			defer close(done)
			b, err := e.start("pull", "", repo)
			if err == nil {
				id = b
			}
		}()
		for j := 0; j < 50; j++ {
			rel, _, ok := reg.TryAcquire([]core.RepoID{repo}, mainlock.Owner{Kind: mainlock.KindBuild, ID: "bld_x"})
			if !ok {
				continue
			}
			e.git.mu.Lock()
			for d, n := range e.git.active {
				if n > 0 {
					dir = d
				}
			}
			e.git.mu.Unlock()
			rel()
			if dir != "" {
				t.Fatalf("iteration %d: a build held the checkout while git ran in %s", i, dir)
			}
			time.Sleep(time.Millisecond)
		}
		<-done
		if id != "" {
			for k := 0; k < 400; k++ {
				if v, ok := e.svc.Get(id); ok && v.Finished {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
}
