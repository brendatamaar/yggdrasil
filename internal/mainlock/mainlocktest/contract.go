package mainlocktest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

type holderReader interface {
	Holder(core.RepoID) (mainlock.Holder, bool)
}

var (
	task1 = mainlock.Owner{Kind: mainlock.KindTask, ID: "t1"}
	task2 = mainlock.Owner{Kind: mainlock.KindTask, ID: "t2"}
	build = mainlock.Owner{Kind: mainlock.KindBuild, ID: "b1"}
	gitA  = mainlock.Owner{Kind: mainlock.KindGitAction, ID: "g1"}
)

func ids(s ...string) []core.RepoID {
	out := make([]core.RepoID, len(s))
	for i, v := range s {
		out[i] = core.RepoID(v)
	}
	return out
}

// MainLockContract runs the shared behaviour suite against factory().
func MainLockContract(t *testing.T, factory func() mainlock.MainLock) {
	t.Run("exclusive per repo", func(t *testing.T) {
		l := factory()
		if _, _, ok := l.TryAcquire(ids("a"), task1); !ok {
			t.Fatal("first acquire must win")
		}
		_, h, ok := l.TryAcquire(ids("a"), task2)
		if ok {
			t.Fatal("second owner must be denied")
		}
		if h.Owner != task1 || h.Repo != "a" {
			t.Fatalf("holder = %+v, want task1 on a", h)
		}
		if _, _, ok := l.TryAcquire(ids("b"), task2); !ok {
			t.Fatal("other repo must be free")
		}
	})
	t.Run("same owner re-acquires and one release frees", func(t *testing.T) {
		l := factory()
		rel, _, _ := l.TryAcquire(ids("a"), task1)
		if _, _, ok := l.TryAcquire(ids("a"), task1); !ok {
			t.Fatal("same owner must re-acquire")
		}
		rel()
		if _, _, ok := l.TryAcquire(ids("a"), task2); !ok {
			t.Fatal("one release must free the repo")
		}
	})
	t.Run("release frees", func(t *testing.T) {
		l := factory()
		rel, _, _ := l.TryAcquire(ids("a", "b"), build)
		rel()
		if _, _, ok := l.TryAcquire(ids("a", "b"), gitA); !ok {
			t.Fatal("repos must be free after release")
		}
	})
	t.Run("double release is safe and does not free a new holder", func(t *testing.T) {
		l := factory()
		rel, _, _ := l.TryAcquire(ids("a"), task1)
		rel()
		if _, _, ok := l.TryAcquire(ids("a"), task2); !ok {
			t.Fatal("task2 must acquire after release")
		}
		rel() // stale
		if _, _, ok := l.TryAcquire(ids("a"), build); ok {
			t.Fatal("stale release freed another owner's lock")
		}
	})
	t.Run("all or nothing", func(t *testing.T) {
		l := factory()
		_, _, _ = l.TryAcquire(ids("b"), task1)
		_, h, ok := l.TryAcquire(ids("a", "b", "c"), task2)
		if ok || h.Repo != "b" {
			t.Fatalf("want denial on b, got ok=%v holder=%+v", ok, h)
		}
		for _, r := range ids("a", "c") {
			if _, _, ok := l.TryAcquire([]core.RepoID{r}, build); !ok {
				t.Fatalf("repo %s was partially held by a failed acquire", r)
			}
		}
	})
	t.Run("duplicate and empty lists", func(t *testing.T) {
		l := factory()
		rel, _, ok := l.TryAcquire(ids("a", "a"), task1)
		if !ok {
			t.Fatal("duplicates must be fine")
		}
		rel()
		if _, _, ok := l.TryAcquire(nil, task1); !ok {
			t.Fatal("empty list must succeed")
		}
	})
	t.Run("build task and git action exclude each other", func(t *testing.T) {
		l := factory()
		rel, _, _ := l.TryAcquire(ids("a"), build)
		if _, h, ok := l.TryAcquire(ids("a"), task1); ok || h.Owner != build {
			t.Fatalf("task must be denied by build, got ok=%v %+v", ok, h)
		}
		rel()
		_, _, _ = l.TryAcquire(ids("a"), task1)
		if _, h, ok := l.TryAcquire(ids("a"), build); ok || h.Owner != task1 {
			t.Fatalf("build must be denied by task, got ok=%v %+v", ok, h)
		}
		if _, _, ok := l.TryAcquire(ids("a"), gitA); ok {
			t.Fatal("git action must be denied")
		}
	})
	t.Run("same ID different kind is a different owner", func(t *testing.T) {
		l := factory()
		_, _, _ = l.TryAcquire(ids("a"), mainlock.Owner{Kind: mainlock.KindTask, ID: "x"})
		if _, _, ok := l.TryAcquire(ids("a"), mainlock.Owner{Kind: mainlock.KindBuild, ID: "x"}); ok {
			t.Fatal("kinds must differ")
		}
	})
	t.Run("concurrent acquirers get exactly one winner", func(t *testing.T) {
		l := factory()
		for round := 0; round < 50; round++ {
			var wins atomic.Int32
			var wg sync.WaitGroup
			var rels sync.Map
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					o := mainlock.Owner{Kind: mainlock.KindBuild, ID: fmt.Sprint(i)}
					list := ids("a", "b")
					if i%2 == 1 {
						list = ids("b", "a")
					}
					if rel, _, ok := l.TryAcquire(list, o); ok {
						wins.Add(1)
						rels.Store(i, rel)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("round %d: %d winners, want 1", round, wins.Load())
			}
			rels.Range(func(_, v any) bool { v.(func())(); return true })
		}
	})
}

// HolderContract checks holder reporting on locks that expose Holder.
func HolderContract(t *testing.T, factory func() mainlock.MainLock) {
	t.Helper()
	l := factory()
	hr, ok := l.(holderReader)
	if !ok {
		t.Skip("lock does not report holders")
	}
	if _, held := hr.Holder("a"); held {
		t.Fatal("fresh lock must have no holder")
	}
	rel, _, _ := l.TryAcquire(ids("a"), build)
	h, held := hr.Holder("a")
	if !held || h.Owner != build || h.Repo != "a" {
		t.Fatalf("holder = %+v, %v", h, held)
	}
	rel()
	if _, held := hr.Holder("a"); held {
		t.Fatal("holder must clear after release")
	}
}
