package landing

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	propRuns  = 300
	propSteps = 80
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

func hx(i int) string { return strings.Repeat(fmt.Sprintf("%x", i%16), 40) }

func pct(r *rand.Rand, n int) bool { return r.Intn(100) < n }

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.Intn(len(xs))] }

// countOf counts the commands of concrete type X in cmds.
func countOf[X any, C any](cmds []C) (n int) {
	for _, c := range cmds {
		if _, ok := any(c).(X); ok {
			n++
		}
	}
	return n
}

func same(a, b any) bool { return reflect.DeepEqual(a, b) }

// ---- merge ----

var mergeReq = MergeReq{
	Repo: "r1", Worktree: "w1", Branch: "ygg/t1", ExpectBranchHead: hx(1),
	TargetBranch: "main", Title: "Add cart", Path: `C:\src\shop api`,
}

type mgen struct {
	r    *rand.Rand
	now  time.Time
	last MergeInput
	mode Mode
}

func (g *mgen) tick() time.Time {
	g.now = g.now.Add(time.Second)
	return g.now
}

func (g *mgen) observed() Observed {
	r := g.r
	o := Observed{
		Tracked:        pct(r, 90),
		Clean:          pct(r, 90),
		DetachedHead:   pct(r, 6),
		CurrentBranch:  "main",
		Head:           hx(3),
		BranchHead:     hx(1),
		AlreadyMerged:  pct(r, 8),
		WorktreeClean:  pct(r, 92),
		WorktreeBranch: mergeReq.Branch,
		Now:            g.tick(),
	}
	if pct(r, 8) {
		o.CurrentBranch = "dev"
	}
	if pct(r, 4) {
		o.WorktreeBranch = "ygg/other"
	}
	switch k := r.Intn(100); {
	case k < 6:
		o.BranchHead = hx(2) // stale
	case k < 10:
		o.BranchHead = ""
	}
	o.MergeInProgress = pct(r, 8)
	if g.mode == ModeRecovering {
		o.MergeInProgress = pct(r, 40)
	}
	if o.MergeInProgress {
		o.MergeHead = pick(r, "", hx(1), hx(1), hx(2))
	}
	return o
}

func (g *mgen) predicted() Predicted {
	var res PredictResult
	switch k := g.r.Intn(100); {
	case k < 40:
		res = PredictClean{}
	case k < 60:
		res = PredictConflicts{Files: []RelPath{"a.go", "b dir/c.txt"}}
	case k < 75:
		res = PredictUnsupported{}
	case k < 95:
		res = PredictError{Msg: "boom"}
	}
	return Predicted{Result: res, Now: g.tick()}
}

func (g *mgen) expected(s MergeState) MergeInput {
	r := g.r
	switch s.(type) {
	case MergeIdle:
		g.mode = pick(r, ModeFresh, ModeFresh, ModeRecovering)
		return MergeStart{Req: mergeReq, Mode: g.mode, Now: g.tick()}
	case MergeChecking, MergeSettling:
		return g.observed()
	case MergePredicting:
		return g.predicted()
	case MergeMerging:
		switch k := r.Intn(10); {
		case k < 5:
			return MergeDone{Commit: hx(9), Now: g.tick()}
		case k < 8:
			return MergeConflicted{Files: []RelPath{"x.go"}, Now: g.tick()}
		}
		return MergeErrored{Reason: pick(r, FailExec, FailIdentityMissing, FailInterrupted, FailReason("")), Msg: "e", Now: g.tick()}
	case MergeAborting:
		if pct(r, 80) {
			return AbortDone{Now: g.tick()}
		}
		return AbortErrored{Msg: "e", Now: g.tick()}
	}
	return g.any()
}

func (g *mgen) any() MergeInput {
	switch g.r.Intn(9) {
	case 0:
		req := mergeReq
		if pct(g.r, 20) {
			req.ExpectBranchHead = "bad"
		}
		g.mode = pick(g.r, ModeFresh, ModeRecovering, Mode("bogus"))
		return MergeStart{Req: req, Mode: g.mode, Now: g.tick()}
	case 1:
		return g.observed()
	case 2:
		return g.predicted()
	case 3:
		return MergeDone{Commit: hx(9), Now: g.tick()}
	case 4:
		return MergeConflicted{Files: []RelPath{"y"}, Now: g.tick()}
	case 5:
		return MergeErrored{Reason: FailExec, Now: g.tick()}
	case 6:
		return AbortDone{Now: g.tick()}
	case 7:
		return AbortErrored{Now: g.tick()}
	}
	return g.observed()
}

func (g *mgen) next(s MergeState) MergeInput {
	if g.last != nil && pct(g.r, 10) {
		return g.last // duplicate
	}
	var in MergeInput
	if pct(g.r, 70) {
		in = g.expected(s)
	} else {
		in = g.any()
	}
	g.last = in
	return in
}

func mergeTerminal(s MergeState) bool {
	switch s.(type) {
	case Merged, AlreadyMerged, MergeRefused, Conflicted, MergeFailed, NeedsHuman:
		return true
	}
	return false
}

// mhist is what the runner knows about the current run, for the checks.
type mhist struct {
	req       MergeReq
	mode      Mode
	obs       Observed // the last Observed accepted while Checking
	haveObs   bool
	runMerges int
	aborts    int
	audits    int
	marks     int
}

type mcheck = func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand)

// runMergeProp drives fresh runs through random inputs and calls check after
// every step. seen, when set, records what the generator reached.
func runMergeProp(t *testing.T, r *rand.Rand, check mcheck, seen map[string]bool) {
	t.Helper()
	g := &mgen{r: r, now: epoch}
	var s MergeState = MergeIdle{}
	h := &mhist{}
	for i := 0; i < propSteps; i++ {
		if mergeTerminal(s) && pct(r, 25) {
			s, h, g.last = MergeIdle{}, &mhist{}, nil
		}
		in := g.next(s)
		after, cmds := StepMerge(s, in)
		if after == nil {
			t.Fatalf("step %d: %T produced a nil state", i, in)
		}
		if v, ok := in.(MergeStart); ok {
			if _, idle := s.(MergeIdle); idle {
				*h = mhist{req: v.Req, mode: v.Mode}
			}
		}
		if o, ok := in.(Observed); ok {
			if _, chk := s.(MergeChecking); chk {
				h.obs, h.haveObs = o, true
			}
		}
		check(t, h, s, in, after, cmds)
		if t.Failed() {
			t.Fatalf("step %d: %T failed the invariant (state %T -> %T)", i, in, s, after)
		}
		h.runMerges += countOf[RunMerge](cmds)
		h.aborts += countOf[Abort](cmds)
		h.audits += countOf[WriteAudit](cmds)
		h.marks += countOf[MarkWorktreeMerged](cmds)
		if seen != nil {
			seen[fmt.Sprintf("%T", after)] = true
			switch v := after.(type) {
			case MergeRefused:
				seen["refused:"+string(v.Reason)] = true
			case MergeFailed:
				seen["failed:"+string(v.Reason)] = true
			case MergeAborting:
				seen[fmt.Sprintf("aborting:%T", v.Cause)] = true
			case MergeSettling:
				seen[fmt.Sprintf("settling:%T", v.Cause)] = true
			case NeedsHuman:
				seen[fmt.Sprintf("needs_human_from:%T", s)] = true
			case Conflicted:
				seen[fmt.Sprintf("conflicted_from:%T", s)] = true
			}
			for _, c := range cmds {
				seen[fmt.Sprintf("%T", c)] = true
			}
		}
		s = after
	}
}

func propMerge(t *testing.T, check mcheck) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) { runMergeProp(t, r, check, nil) })
}

// ---- remove ----

var removeReq = RemoveReq{
	Worktree: "w1", Repo: "r1", Path: `D:\wt\shop api-ab12`, Branch: "ygg/t1",
	Root: `D:\wt`, Name: "shop api-ab12",
}

type rgen struct {
	r    *rand.Rand
	now  time.Time
	last RemoveInput
}

func (g *rgen) tick() time.Time {
	g.now = g.now.Add(time.Second)
	return g.now
}

func (g *rgen) observed() RemoveObserved {
	r := g.r
	o := RemoveObserved{
		UnderRoot:      pct(r, 94),
		IsMain:         pct(r, 4),
		RowStatus:      pick(r, core.WorktreeActive, core.WorktreeActive, core.WorktreeMerged, core.WorktreeRemoving, core.WorktreeRemoved, core.WorktreeStatus("")),
		PathExists:     pct(r, 90),
		StagedExists:   pct(r, 14),
		Clean:          pct(r, 90),
		StatusReadable: pct(r, 94),
		TaskLive:       pct(r, 6),
		BuildLive:      pct(r, 6),
		OtherPrunable:  pct(r, 30),
		Now:            g.tick(),
	}
	if o.StagedExists {
		o.Staged = StagePath{Root: removeReq.Root, Leaf: "shop_api-ab12-" + pick(r, "st9", "old1", "2-old1", "2-st9")}
		switch k := r.Intn(100); {
		case k < 10:
			o.Staged.Root = `E:\elsewhere`
		case k < 20:
			o.Staged.Leaf = pick(r, "other-1", "..", "", `a/b-1`, "shop_api-ab12-", "shop_api-ab12-x y")
		}
	}
	return o
}

func (g *rgen) holders() []string {
	pool := []string{"Code.exe", "code.exe", "node.exe", "  ", "term\x1b[31minal.exe", "a\nb.exe", "go.exe", "git.exe", "pwsh.exe", "java.exe", strings20()}
	n := g.r.Intn(9)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pool[g.r.Intn(len(pool))])
	}
	return out
}

func strings20() string { return strings.Repeat("x", 90) }

func (g *rgen) class() LockClass {
	return pick(g.r, LockSharing, LockAccessDenied, LockPathTooLong, LockOther, LockClass(""), LockClass("bogus"))
}

func (g *rgen) expected(s RemoveState) RemoveInput {
	r := g.r
	switch s.(type) {
	case RemoveIdle:
		req := removeReq
		if pct(r, 40) {
			req.StagedLeaf = "shop_api-ab12-old1"
		}
		id := "st9"
		if pct(r, 4) {
			id = "a/b"
		}
		if pct(r, 3) {
			req.Root = ""
		}
		return RemoveStart{Req: req, StageID: id, Now: g.tick()}
	case RemoveChecking:
		return g.observed()
	case Moving:
		if pct(r, 60) {
			return Moved{Now: g.tick()}
		}
		return MoveFailed{Class: g.class(), Holders: g.holders(), Now: g.tick()}
	case Deleting:
		if pct(r, 60) {
			return Deleted{Now: g.tick()}
		}
		return DeleteFailed{Class: g.class(), Holders: g.holders(), Remaining: r.Intn(9) - 2, Now: g.tick()}
	case Pruning:
		if pct(r, 70) {
			return Pruned{Now: g.tick()}
		}
		return PruneFailed{Msg: "e", Now: g.tick()}
	}
	return g.any()
}

func (g *rgen) any() RemoveInput {
	switch g.r.Intn(8) {
	case 0:
		return RemoveStart{Req: removeReq, StageID: "st9", Now: g.tick()}
	case 1:
		return g.observed()
	case 2:
		return Moved{Now: g.tick()}
	case 3:
		return MoveFailed{Class: g.class(), Holders: g.holders(), Now: g.tick()}
	case 4:
		return Deleted{Now: g.tick()}
	case 5:
		return DeleteFailed{Class: g.class(), Holders: g.holders(), Now: g.tick()}
	case 6:
		return Pruned{Now: g.tick()}
	}
	return PruneFailed{Msg: "e", Now: g.tick()}
}

func (g *rgen) next(s RemoveState) RemoveInput {
	if g.last != nil && pct(g.r, 10) {
		return g.last
	}
	var in RemoveInput
	if pct(g.r, 70) {
		in = g.expected(s)
	} else {
		in = g.any()
	}
	g.last = in
	return in
}

func removeTerminal(s RemoveState) bool {
	switch s.(type) {
	case Removed, RemoveRefused, Locked, Stuck, RemoveFailed:
		return true
	}
	return false
}

type rhist struct {
	req          RemoveReq
	stageID      string
	obs          RemoveObserved
	haveObs      bool
	deletes      int
	markRemoving int
	markRemoved  int
	prunes       int
	audits       int
	removedFirst bool // MarkRemoved was emitted before MarkRemoving
}

type rcheck = func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand)

func runRemoveProp(t *testing.T, r *rand.Rand, check rcheck, seen map[string]bool) {
	t.Helper()
	g := &rgen{r: r, now: epoch}
	var s RemoveState = RemoveIdle{}
	h := &rhist{}
	for i := 0; i < propSteps; i++ {
		if removeTerminal(s) && pct(r, 25) {
			s, h, g.last = RemoveIdle{}, &rhist{}, nil
		}
		in := g.next(s)
		after, cmds := StepRemove(s, in)
		if after == nil {
			t.Fatalf("step %d: %T produced a nil state", i, in)
		}
		if v, ok := in.(RemoveStart); ok {
			if _, idle := s.(RemoveIdle); idle {
				*h = rhist{req: v.Req, stageID: v.StageID}
			}
		}
		if o, ok := in.(RemoveObserved); ok {
			if _, chk := s.(RemoveChecking); chk {
				h.obs, h.haveObs = o, true
			}
		}
		check(t, h, s, in, after, cmds)
		if t.Failed() {
			t.Fatalf("step %d: %T failed the invariant (state %T -> %T)", i, in, s, after)
		}
		h.deletes += countOf[DeleteStaged](cmds)
		h.prunes += countOf[PruneWorktrees](cmds)
		h.audits += countOf[WriteAudit](cmds)
		for _, c := range cmds {
			switch c.(type) {
			case MarkRemoving:
				h.markRemoving++
				if h.markRemoved > 0 {
					h.removedFirst = true
				}
			case MarkRemoved:
				h.markRemoved++
			}
		}
		if seen != nil {
			seen[fmt.Sprintf("%T", after)] = true
			switch v := after.(type) {
			case RemoveRefused:
				seen["refused:"+string(v.Reason)] = true
			case Removed:
				seen["removed:"+v.Note] = true
			case Deleting:
				seen[fmt.Sprintf("deleting_from:%T", s)] = true
			}
			for _, c := range cmds {
				seen[fmt.Sprintf("%T", c)] = true
			}
		}
		s = after
	}
}

func propRemove(t *testing.T, check rcheck) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) { runRemoveProp(t, r, check, nil) })
}
