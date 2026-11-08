package worktreeenv

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= 300; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

// smallRange holds 5 blocks so the table fills up quickly.
var smallRange = Range{Lo: 2000, Hi: 2099}

func randWT(r *rand.Rand) core.WorktreeID { return core.WorktreeID(fmt.Sprintf("wt_%d", r.Intn(9))) }

func randBusy(r *rand.Rand, rg Range) []int {
	var out []int
	for range r.Intn(4) {
		out = append(out, rg.Lo-5+r.Intn(rg.Hi-rg.Lo+10))
	}
	return out
}

func randAllocInput(r *rand.Rand, rg Range) AllocInput {
	switch r.Intn(10) {
	case 0, 1, 2, 3, 4:
		return AllocateRequested{Worktree: randWT(r), Busy: randBusy(r, rg), Now: epoch}
	case 5, 6, 7:
		return Released{Worktree: randWT(r), Now: epoch}
	case 8:
		lo := 1000 + r.Intn(2000)
		return RangeChanged{Range: Range{Lo: lo, Hi: lo + r.Intn(150)}}
	}
	blocks := map[core.WorktreeID]Block{}
	for range r.Intn(5) {
		blocks[randWT(r)] = Block{Base: 1990 + r.Intn(120), Size: []int{20, 20, 10, 0, 40}[r.Intn(5)]}
	}
	return Loaded{Blocks: blocks}
}

func deepCopyTable(t Table) Table {
	c := Table{Range: t.Range, Live: map[core.WorktreeID]Block{}}
	for k, v := range t.Live {
		c.Live[k] = v
	}
	return c
}

// walk runs random inputs and calls check after each step with the table
// before, the input, the table after and the commands.
func walk(t *testing.T, r *rand.Rand, check func(before Table, in AllocInput, after Table, cmds []AllocCommand)) {
	t.Helper()
	tab := Table{Range: smallRange, Live: map[core.WorktreeID]Block{}}
	for range 80 {
		in := randAllocInput(r, tab.Range)
		before := deepCopyTable(tab)
		snapshot := deepCopyTable(tab)
		after, cmds := Allocate(tab, in)
		if !reflect.DeepEqual(tab, snapshot) {
			t.Fatalf("Allocate changed its input table for %#v", in)
		}
		check(before, in, after, cmds)
		tab = after
	}
}

func TestPropBlocksDisjoint(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(_ Table, in AllocInput, after Table, _ []AllocCommand) {
			for a, ba := range after.Live {
				for b, bb := range after.Live {
					if a != b && ba.Base < bb.Base+bb.Size && bb.Base < ba.Base+ba.Size {
						t.Fatalf("%s %v overlaps %s %v after %#v", a, ba, b, bb, in)
					}
				}
			}
		})
	})
}

func TestPropAllocInsideRangeAndFree(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, after Table, _ []AllocCommand) {
			req, ok := in.(AllocateRequested)
			if !ok {
				return
			}
			if _, had := before.Live[req.Worktree]; had {
				return
			}
			b, got := after.Live[req.Worktree]
			if !got {
				return
			}
			if b.Base < before.Range.Lo || b.Base+b.Size-1 > before.Range.Hi || b.Size != core.PortBlockSize ||
				(b.Base-before.Range.Lo)%core.PortBlockSize != 0 {
				t.Fatalf("block %v is not an aligned block of %v", b, before.Range)
			}
			for _, p := range req.Busy {
				if b.Contains(p) {
					t.Fatalf("block %v holds busy port %d", b, p)
				}
			}
		})
	})
}

func TestPropAllocIdempotent(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, after Table, cmds []AllocCommand) {
			req, ok := in.(AllocateRequested)
			if !ok {
				return
			}
			had, lived := before.Live[req.Worktree]
			if !lived {
				return
			}
			if after.Live[req.Worktree] != had || len(after.Live) != len(before.Live) {
				t.Fatalf("second request moved or added blocks")
			}
			if len(cmds) != 1 || cmds[0] != (AllocReply{Worktree: req.Worktree, Block: had}) {
				t.Fatalf("want a single reply with the same block, got %#v", cmds)
			}
		})
	})
}

func TestPropRangeFullExact(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, _ Table, cmds []AllocCommand) {
			req, ok := in.(AllocateRequested)
			if !ok || req.Worktree == "" {
				return
			}
			if _, had := before.Live[req.Worktree]; had {
				return
			}
			refused := len(cmds) == 1 && cmds[0] == (AllocReply{Worktree: req.Worktree, Refusal: RefusedRangeFull})
			free := false
			for base := before.Range.Lo; base+19 <= before.Range.Hi; base += 20 {
				b := Block{Base: base, Size: 20}
				ok := true
				for _, o := range before.Live {
					if b.Base < o.Base+o.Size && o.Base < b.Base+b.Size {
						ok = false
					}
				}
				for _, p := range req.Busy {
					if b.Contains(p) {
						ok = false
					}
				}
				free = free || ok
			}
			if refused == free {
				t.Fatalf("refused=%v but a free block exists=%v", refused, free)
			}
		})
	})
}

func TestPropRangeChangeKeepsBlocks(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, after Table, _ []AllocCommand) {
			if _, ok := in.(RangeChanged); ok && !reflect.DeepEqual(before.Live, after.Live) {
				t.Fatalf("RangeChanged changed Live")
			}
		})
	})
}

func TestPropReleaseIdempotent(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, after Table, cmds []AllocCommand) {
			rel, ok := in.(Released)
			if !ok {
				return
			}
			if _, had := before.Live[rel.Worktree]; !had {
				if len(cmds) != 0 || !reflect.DeepEqual(before, after) {
					t.Fatalf("release of an unknown worktree did something: %#v", cmds)
				}
				return
			}
			if _, still := after.Live[rel.Worktree]; still {
				t.Fatalf("block still live after release")
			}
			// a second release does nothing
			twice, c2 := Allocate(after, rel)
			if len(c2) != 0 || !reflect.DeepEqual(after, twice) {
				t.Fatalf("second release did something")
			}
		})
	})
}

func TestAllocCommandsPersistAndAudit(t *testing.T) {
	tab := Table{Range: smallRange, Live: map[core.WorktreeID]Block{}}
	tab, cmds := Allocate(tab, AllocateRequested{Worktree: "wt_a", Now: epoch})
	want := Block{Base: 2000, Size: 20}
	if tab.Live["wt_a"] != want {
		t.Fatalf("got %v", tab.Live["wt_a"])
	}
	exp := []AllocCommand{
		PersistBlock{Worktree: "wt_a", Block: want},
		AllocAudit{Action: ActionPorts, Worktree: "wt_a", Block: want},
		AllocReply{Worktree: "wt_a", Block: want},
	}
	if !reflect.DeepEqual(cmds, exp) {
		t.Fatalf("got %#v", cmds)
	}
	// busy port in the first block moves the next worktree to the second
	_, cmds = Allocate(tab, AllocateRequested{Worktree: "wt_b", Busy: []int{2025}})
	if r := cmds[len(cmds)-1].(AllocReply); r.Block.Base != 2040 {
		t.Fatalf("busy port in block 2 not skipped: %v", r.Block)
	}
}

func TestLoadedDropsOverlaps(t *testing.T) {
	tab := Table{Range: smallRange}
	got, cmds := Allocate(tab, Loaded{Blocks: map[core.WorktreeID]Block{
		"wt_a": {Base: 2000, Size: 20}, "wt_b": {Base: 2010, Size: 20}, "wt_c": {Base: 2020, Size: 20},
	}})
	if len(got.Live) != 2 || got.Live["wt_b"] != (Block{}) {
		t.Fatalf("live = %v", got.Live)
	}
	if len(cmds) != 1 || cmds[0].(AllocAudit).Action != ActionConflict {
		t.Fatalf("cmds = %#v", cmds)
	}
}

func TestRangeChangedInvalidIgnored(t *testing.T) {
	tab := Table{Range: smallRange, Live: map[core.WorktreeID]Block{}}
	for _, bad := range []Range{{Lo: 10, Hi: 500}, {Lo: 2000, Hi: 2010}, {Lo: 2000, Hi: 70000}} {
		got, cmds := Allocate(tab, RangeChanged{Range: bad})
		if got.Range != smallRange || len(cmds) != 0 {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestReleasedBlockIsReused(t *testing.T) {
	tab := Table{Range: Range{Lo: 2000, Hi: 2019}, Live: map[core.WorktreeID]Block{}}
	tab, _ = Allocate(tab, AllocateRequested{Worktree: "wt_a"})
	_, cmds := Allocate(tab, AllocateRequested{Worktree: "wt_b"})
	if cmds[0].(AllocReply).Refusal != RefusedRangeFull {
		t.Fatal("range should be full")
	}
	tab, _ = Allocate(tab, Released{Worktree: "wt_a"})
	tab, _ = Allocate(tab, AllocateRequested{Worktree: "wt_b"})
	if tab.Live["wt_b"].Base != 2000 {
		t.Fatalf("block not reused: %v", tab.Live)
	}
}
