package landing

import (
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func expectedStage(h *rhist) StagePath {
	p, _ := StagingPath(h.req.Root, h.req.Name, h.stageID)
	return p
}

// R1: MoveToStaging only after an observation that passed every check, and
// only to the planned staging path.
func TestPropMoveOnlyAfterChecks(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		for _, c := range cmds {
			mv, ok := c.(MoveToStaging)
			if !ok {
				continue
			}
			o, isObs := in.(RemoveObserved)
			if _, chk := before.(RemoveChecking); !chk || !isObs {
				t.Fatalf("MoveToStaging from %T on %T", before, in)
			}
			if !o.UnderRoot || o.IsMain || !o.Clean || !o.StatusReadable || o.TaskLive || o.BuildLive {
				t.Errorf("MoveToStaging after %+v", o)
			}
			if mv.To != expectedStage(h) || mv.Req != h.req {
				t.Errorf("MoveToStaging carries %+v", mv)
			}
		}
	})
}

// R2: DeleteStaged only after Moved, or when the observation found a staged
// folder from an earlier run.
func TestPropDeleteOnlyAfterMove(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		if countOf[DeleteStaged](cmds) == 0 {
			return
		}
		switch before.(type) {
		case Moving:
			if _, ok := in.(Moved); !ok {
				t.Errorf("DeleteStaged from Moving on %T", in)
			}
		case RemoveChecking:
			if o, ok := in.(RemoveObserved); !ok || !o.StagedExists || o.PathExists {
				t.Errorf("DeleteStaged on %+v without a staged folder", in)
			}
		default:
			t.Errorf("DeleteStaged from %T", before)
		}
	})
}

func validLeafIndependent(leaf string) bool {
	return leaf != "" && leaf != "." && leaf != ".." && !strings.HasPrefix(leaf, ".") &&
		!strings.ContainsAny(leaf, `/\:`) && strings.Contains(leaf, "-")
}

// R3: the path of DeleteStaged is the planned staging path or the observed
// staged path under the staging folder of this worktree root; never Req.Path.
func TestPropDeletePathIsStaged(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		for _, c := range cmds {
			var p StagePath
			switch v := c.(type) {
			case DeleteStaged:
				p = v.Staged
			case MoveToStaging:
				p = v.To
			case MarkRemoving:
				p = v.Staged
			default:
				continue
			}
			if p.Root != h.req.Root || !validLeafIndependent(p.Leaf) || !strings.HasPrefix(p.Leaf, "shop_api-ab12-") {
				t.Errorf("%T path %+v is not a staged folder of this worktree", c, p)
			}
			if got := filepath.Join(p.Root, StagingDir, p.Leaf); filepath.Dir(got) != filepath.Join(h.req.Root, StagingDir) || got == filepath.Clean(h.req.Path) {
				t.Errorf("%T would reach %q", c, got)
			}
			if d, ok := c.(DeleteStaged); ok {
				if _, moved := before.(Moving); moved && d.Staged != expectedStage(h) {
					t.Errorf("DeleteStaged after Moved is %+v, not the planned path", d.Staged)
				}
				if _, chk := before.(RemoveChecking); chk && d.Staged != h.obs.Staged {
					t.Errorf("DeleteStaged is %+v, not the observed one %+v", d.Staged, h.obs.Staged)
				}
			}
		}
	})
}

// R4: a Refused or Locked run emits no DeleteStaged and no MarkRemoving.
func TestPropRefusedDeletesNothing(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		if removeTerminal(before) {
			return
		}
		switch after.(type) {
		case RemoveRefused, Locked:
		default:
			return
		}
		if h.deletes != 0 || h.markRemoving != 0 {
			t.Errorf("%T after DeleteStaged/MarkRemoving", after)
		}
		for _, c := range cmds {
			if _, ok := c.(WriteAudit); !ok {
				t.Errorf("%T emitted %T", after, c)
			}
		}
	})
}

// R5: PruneWorktrees never when OtherPrunable was true.
func TestPropNoPruneWithOthers(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		if countOf[PruneWorktrees](cmds) > 0 {
			if !h.haveObs || h.obs.OtherPrunable {
				t.Errorf("PruneWorktrees with OtherPrunable=%v", h.obs.OtherPrunable)
			}
			if _, ok := in.(Deleted); !ok {
				t.Errorf("PruneWorktrees on %T", in)
			}
		}
		if _, ok := after.(Pruning); ok && h.obs.OtherPrunable {
			t.Errorf("Pruning with OtherPrunable")
		}
	})
}

// R6: only the seven allowed commands.
func TestPropRemoveCommandsClosed(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		for _, c := range cmds {
			switch c.(type) {
			case ObserveWorktree, MoveToStaging, MarkRemoving, DeleteStaged, PruneWorktrees, MarkRemoved, WriteAudit:
			default:
				t.Errorf("command %T is not in the remove set", c)
			}
		}
	})
}

// R7: Locked and Stuck carry a valid class, a non-empty message, and holders
// that are unique, capped and printable.
func TestPropLockMessage(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		var class LockClass
		var holders []string
		var msg string
		switch v := after.(type) {
		case Locked:
			class, holders, msg = v.Class, v.Holders, v.Message()
		case Stuck:
			class, holders, msg = v.Class, v.Holders, v.Message()
		default:
			return
		}
		if class.Validate() != nil {
			t.Errorf("class %q", class)
		}
		if strings.TrimSpace(msg) == "" {
			t.Errorf("empty message")
		}
		if len(holders) > MaxHolders {
			t.Errorf("%d holders", len(holders))
		}
		seen := map[string]bool{}
		for _, hd := range holders {
			k := strings.ToLower(hd)
			if hd == "" || seen[k] {
				t.Errorf("holder %q empty or duplicate", hd)
			}
			seen[k] = true
			for _, r := range hd {
				if unicode.IsControl(r) {
					t.Errorf("holder %q has a control character", hd)
				}
			}
		}
		for _, r := range msg {
			if unicode.IsControl(r) {
				t.Errorf("message has a control character: %q", msg)
			}
		}
	})
}

// R8: the row status only moves forward: MarkRemoving at most once and never
// after MarkRemoved; MarkRemoved at most once.
func TestPropRowStatusMonotone(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		mr, md := h.markRemoving, h.markRemoved
		for _, c := range cmds {
			switch c.(type) {
			case MarkRemoving:
				mr++
				if md > 0 {
					t.Errorf("MarkRemoving after MarkRemoved")
				}
			case MarkRemoved:
				md++
			}
		}
		if mr > 1 || md > 1 {
			t.Errorf("MarkRemoving x%d, MarkRemoved x%d in one run", mr, md)
		}
		if removeTerminal(before) && len(cmds) != 0 {
			t.Errorf("terminal %T emitted commands", before)
		}
		if r, ok := after.(Removed); ok && !removeTerminal(before) {
			rowAlreadyRemoved := r.Note == noteAlreadyRemoved && h.obs.RowStatus == core.WorktreeRemoved
			if (rowAlreadyRemoved && md != 0) || (!rowAlreadyRemoved && md != 1) {
				t.Errorf("Removed with %d MarkRemoved (note %q)", md, r.Note)
			}
		}
		if h.removedFirst {
			t.Errorf("MarkRemoved came before MarkRemoving")
		}
	})
}

// Terminal states never change and each run writes exactly one audit.
func TestPropRemoveTerminalFinalOneAudit(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		if removeTerminal(before) && (!same(before, after) || len(cmds) != 0) {
			t.Errorf("terminal %T changed to %T on %T", before, after, in)
		}
		n := countOf[WriteAudit](cmds)
		enters := removeTerminal(after) && !removeTerminal(before)
		if (enters && n != 1) || (!enters && n != 0) || h.audits+n > 1 {
			t.Errorf("%T -> %T wrote %d audits (%d before)", before, after, n, h.audits)
		}
	})
}

// R9: StagingPath never lets Name leave Root/.removing, and is unique per id.
func TestPropStagingPathInside(t *testing.T) {
	names := []string{"", ".", "..", "../..", `..\x`, "a/b", `a\b`, "C:evil", "con", "x y", "é", "a\x00b", "a\nb", strings.Repeat("n", 300), ".hidden", "ok-1"}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		root := filepath.Join(t.TempDir(), "wt root")
		name := names[r.Intn(len(names))]
		if pct(r, 50) {
			name += string(rune(r.Intn(0x2fff))) + names[r.Intn(len(names))]
		}
		id1, id2 := "id"+strings.Repeat("a", r.Intn(5)), "id"+strings.Repeat("b", r.Intn(5))
		a, err := StagingPath(root, name, id1)
		if err != nil {
			t.Fatalf("StagingPath(%q): %v", name, err)
		}
		b, _ := StagingPath(root, name, id2)
		if a.Root != root || strings.ContainsAny(a.Leaf, `/\:`) || strings.HasPrefix(a.Leaf, ".") {
			t.Fatalf("unsafe leaf %q for name %q", a.Leaf, name)
		}
		full := filepath.Join(a.Root, StagingDir, a.Leaf)
		if filepath.Dir(full) != filepath.Join(root, StagingDir) {
			t.Errorf("%q is not directly under the staging folder", full)
		}
		if rel, err := filepath.Rel(root, full); err != nil || !strings.HasPrefix(rel, StagingDir+string(filepath.Separator)) {
			t.Errorf("%q is not under %s (rel %q, %v)", full, StagingDir, rel, err)
		}
		if id1 != id2 && a.Leaf == b.Leaf {
			t.Errorf("ids %q and %q gave one leaf", id1, id2)
		}
	})
}

// The generator reaches every state, command and refusal.
func TestRemoveGeneratorCoverage(t *testing.T) {
	seen := map[string]bool{}
	for seed := int64(1); seed <= propRuns; seed++ {
		runRemoveProp(t, rand.New(rand.NewSource(seed)), func(*testing.T, *rhist, RemoveState, RemoveInput, RemoveState, []RemoveCommand) {}, seen)
	}
	want := []string{
		"landing.RemoveChecking", "landing.Moving", "landing.Deleting", "landing.Pruning", "landing.Removed", "landing.RemoveRefused",
		"landing.Locked", "landing.Stuck", "landing.RemoveFailed",
		"landing.ObserveWorktree", "landing.MoveToStaging", "landing.MarkRemoving", "landing.DeleteStaged", "landing.PruneWorktrees",
		"landing.MarkRemoved", "landing.WriteAudit",
		"refused:outside_root", "refused:main_checkout", "refused:task_running", "refused:in_use", "refused:dirty", "refused:unreadable",
		"removed:", "removed:" + noteAlreadyRemoved, "removed:" + noteGitCleanupLater,
		"deleting_from:landing.Moving", "deleting_from:landing.RemoveChecking",
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("the generator never reached %s", w)
		}
	}
}

// R3b: a staged folder is only resumed when it is exactly the remembered leaf
// (else the planned one) and the live folder is gone; a name that merely starts
// with this worktree's name (a vs a-b) is never claimed.
func TestPropStagedClaimedOnlyExactAndAlone(t *testing.T) {
	propRemove(t, func(t *testing.T, h *rhist, before RemoveState, in RemoveInput, after RemoveState, cmds []RemoveCommand) {
		o, ok := in.(RemoveObserved)
		if _, chk := before.(RemoveChecking); !chk || !ok || !h.haveObs {
			return
		}
		if _, ok := after.(Deleting); !ok {
			return
		}
		want := expectedStage(h)
		if h.req.StagedLeaf != "" {
			want = StagePath{Root: h.req.Root, Leaf: h.req.StagedLeaf}
		}
		if !o.StagedExists || o.PathExists || o.Staged != want {
			t.Errorf("resumed %+v (exists %v, live %v), want exactly %+v", o.Staged, o.StagedExists, o.PathExists, want)
		}
	})
}
