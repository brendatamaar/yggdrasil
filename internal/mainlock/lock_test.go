package mainlock_test

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/mainlock/mainlocktest"
)

func TestRegistryContract(t *testing.T) {
	f := func() mainlock.MainLock { return &mainlock.Registry{} }
	mainlocktest.MainLockContract(t, f)
	mainlocktest.HolderContract(t, f)
}

func TestFakeContract(t *testing.T) {
	mainlocktest.MainLockContract(t, func() mainlock.MainLock { return &mainlocktest.Fake{} })
}

func TestFakeScriptedDenial(t *testing.T) {
	f := &mainlocktest.Fake{}
	by := mainlock.Owner{Kind: mainlock.KindGitAction, ID: "g"}
	me := mainlock.Owner{Kind: mainlock.KindTask, ID: "t"}
	f.Deny("a", by)
	_, h, ok := f.TryAcquire([]core.RepoID{"b", "a"}, me)
	if ok || h.Owner != by {
		t.Fatalf("want scripted denial, got ok=%v %+v", ok, h)
	}
	f.Allow("a")
	if _, _, ok := f.TryAcquire([]core.RepoID{"b", "a"}, me); !ok {
		t.Fatal("must acquire after Allow")
	}
}

func TestZeroValueUsable(t *testing.T) {
	var r mainlock.Registry
	if _, ok := r.Holder("x"); ok {
		t.Fatal("zero value has no holders")
	}
	if _, _, ok := r.TryAcquire([]core.RepoID{"x"}, mainlock.Owner{Kind: mainlock.KindTask, ID: "1"}); !ok {
		t.Fatal("zero value must acquire")
	}
}

// Random sequences: no repo has two holders, a failed acquire changes
// nothing, and the registry agrees with a simple model after every step.
func TestRandomSequencesProperty(t *testing.T) {
	repos := []core.RepoID{"a", "b", "c", "d"}
	owners := []mainlock.Owner{
		{Kind: mainlock.KindTask, ID: "1"}, {Kind: mainlock.KindTask, ID: "2"},
		{Kind: mainlock.KindBuild, ID: "1"}, {Kind: mainlock.KindGitAction, ID: "1"},
	}
	type acq struct {
		rel   func()
		ids   []core.RepoID
		owner mainlock.Owner
	}
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var r mainlock.Registry
		model := map[core.RepoID]mainlock.Owner{}
		var live []acq
		for step := 0; step < 100; step++ {
			if rng.Intn(3) == 0 && len(live) > 0 {
				i := rng.Intn(len(live))
				a := live[i]
				a.rel()
				for _, id := range a.ids {
					if model[id] == a.owner {
						delete(model, id)
					}
				}
				if rng.Intn(2) == 0 {
					a.rel() // double release
				}
				live = append(live[:i], live[i+1:]...)
			} else {
				var list []core.RepoID
				for _, id := range repos {
					if rng.Intn(2) == 0 {
						list = append(list, id)
					}
				}
				o := owners[rng.Intn(len(owners))]
				rel, h, ok := r.TryAcquire(list, o)
				conflict := false
				for _, id := range list {
					if cur, taken := model[id]; taken && cur != o {
						conflict = true
					}
				}
				if ok == conflict {
					t.Fatalf("seed %d step %d: ok=%v but model conflict=%v", seed, step, ok, conflict)
				}
				if ok {
					for _, id := range list {
						model[id] = o
					}
					live = append(live, acq{rel, list, o})
				} else if model[h.Repo] != h.Owner {
					t.Fatalf("seed %d: denial holder %+v disagrees with model", seed, h)
				}
			}
			for _, id := range repos {
				h, held := r.Holder(id)
				m, mh := model[id]
				if held != mh || (held && h.Owner != m) {
					t.Fatalf("seed %d step %d: repo %s registry=%+v/%v model=%+v/%v", seed, step, id, h, held, m, mh)
				}
			}
		}
	}
}
