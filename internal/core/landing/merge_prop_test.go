package landing

import (
	"math/rand"
	"testing"
)

func allChecksPass(o Observed, req MergeReq) bool {
	return o.Tracked && o.Clean && !o.MergeInProgress && !o.DetachedHead && o.CurrentBranch == req.TargetBranch &&
		o.BranchHead == req.ExpectBranchHead && o.WorktreeClean && !o.AlreadyMerged
}

// L1: RunMerge only after an Observed that passed every check and a Predicted
// of Clean or Unsupported, with the message the core builds.
func TestPropMergeOnlyAfterAllChecks(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		for _, c := range cmds {
			rm, ok := c.(RunMerge)
			if !ok {
				continue
			}
			if !h.haveObs || !allChecksPass(h.obs, h.req) {
				t.Errorf("RunMerge after an observation that did not pass: %+v", h.obs)
			}
			p, isP := in.(Predicted)
			if !isP {
				t.Fatalf("RunMerge emitted by %T", in)
			}
			switch p.Result.(type) {
			case PredictClean, PredictUnsupported:
			default:
				t.Errorf("RunMerge after prediction %T", p.Result)
			}
			if _, ok := before.(MergePredicting); !ok {
				t.Errorf("RunMerge from %T", before)
			}
			if rm.Req != h.req || rm.Message != MergeMessage(h.req.Branch, h.req.Title) {
				t.Errorf("RunMerge carries %+v", rm)
			}
		}
	})
}

// L2: at most one RunMerge per run.
func TestPropOneMergeCommand(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if n := h.runMerges + countOf[RunMerge](cmds); n > 1 {
			t.Errorf("%d RunMerge in one run", n)
		}
	})
}

// L3: only the six allowed commands ever appear.
func TestPropMergeCommandsClosed(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		for _, c := range cmds {
			switch c.(type) {
			case Observe, Predict, RunMerge, Abort, MarkWorktreeMerged, WriteAudit:
			default:
				t.Errorf("command %T is not in the merge set", c)
			}
		}
	})
}

// L4: Abort only after RunMerge, or in Recovering mode for our own merge.
func TestPropAbortOnlyOurs(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if countOf[Abort](cmds) == 0 {
			return
		}
		switch before.(type) {
		case MergeMerging:
			if _, ok := in.(MergeConflicted); !ok || h.runMerges != 1 {
				t.Errorf("Abort from Merging on %T after %d RunMerge", in, h.runMerges)
			}
		case MergeChecking:
			o, ok := in.(Observed)
			if !ok || h.mode != ModeRecovering || !o.MergeInProgress || o.MergeHead == "" || o.MergeHead != o.BranchHead {
				t.Errorf("Abort on %+v in mode %q: not our own merge", in, h.mode)
			}
			if h.runMerges != 0 {
				t.Errorf("recovery Abort in a run that already merged")
			}
		default:
			t.Errorf("Abort from %T", before)
		}
	})
}

// L5a: terminal states never change and emit nothing.
func TestPropMergeTerminalFinal(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if mergeTerminal(before) && (!same(before, after) || len(cmds) != 0) {
			t.Errorf("terminal %T changed to %T with %d commands on %T", before, after, len(cmds), in)
		}
	})
}

// L5b: each run writes exactly one audit entry, on reaching its terminal state.
func TestPropOneAuditPerTerminal(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		n := countOf[WriteAudit](cmds)
		enters := mergeTerminal(after) && !mergeTerminal(before)
		if enters && n != 1 {
			t.Errorf("entering %T wrote %d audits", after, n)
		}
		if !enters && n != 0 {
			t.Errorf("%T -> %T wrote %d audits", before, after, n)
		}
		if h.audits+n > 1 {
			t.Errorf("%d audits in one run", h.audits+n)
		}
	})
}

// L6: Refused, AlreadyMerged and a conflict found by prediction happen with
// no RunMerge, and change nothing but bookkeeping.
func TestPropNoMergeNoChange(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if mergeTerminal(before) {
			return
		}
		_, predicted := before.(MergePredicting)
		switch after.(type) {
		case MergeRefused, AlreadyMerged:
		case Conflicted:
			if !predicted {
				return
			}
		default:
			return
		}
		if h.runMerges != 0 || countOf[RunMerge](cmds) != 0 || countOf[Abort](cmds) != 0 {
			t.Errorf("%T reached after a merge was started", after)
		}
		for _, c := range cmds {
			switch c.(type) {
			case WriteAudit, MarkWorktreeMerged:
			default:
				t.Errorf("%T reached with command %T", after, c)
			}
		}
	})
}

// L7: Merged only from Merging on MergeDone, with exactly one
// MarkWorktreeMerged; MarkWorktreeMerged appears nowhere else but
// AlreadyMerged.
func TestPropMergedOnlyFromMerging(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if mergeTerminal(before) {
			return
		}
		_, merging := before.(MergeMerging)
		_, done := in.(MergeDone)
		_, isMerged := after.(Merged)
		if isMerged && (!merging || !done || h.runMerges != 1) {
			t.Errorf("Merged from %T on %T", before, in)
		}
		if merging && done && !isMerged {
			t.Errorf("MergeDone in Merging gave %T", after)
		}
		marks := countOf[MarkWorktreeMerged](cmds)
		_, already := after.(AlreadyMerged)
		switch {
		case isMerged || already:
			if marks != 1 {
				t.Errorf("%T with %d MarkWorktreeMerged", after, marks)
			}
		case marks != 0:
			t.Errorf("%T with MarkWorktreeMerged", after)
		}
	})
}

// L8: after a RunMerge, Conflicted/MergeFailed need a clean observation; a
// dirty tree or a merge in progress gives NeedsHuman.
func TestPropSettleNeedsCleanTree(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if mergeTerminal(before) {
			return
		}
		_, afterMerge := before.(MergeSettling)
		if h.runMerges > 0 {
			switch after.(type) {
			case Conflicted, MergeFailed:
				o, ok := in.(Observed)
				if !afterMerge || !ok || !o.Clean || o.MergeInProgress {
					t.Errorf("%T after a merge without a clean observation (%T, %+v)", after, in, in)
				}
			}
		}
		if s, ok := before.(MergeSettling); ok {
			o, isObs := in.(Observed)
			if !isObs {
				return
			}
			_, human := after.(NeedsHuman)
			if clean := o.Clean && !o.MergeInProgress; clean == human {
				t.Errorf("settle with clean=%v gave %T (cause %T)", clean, after, s.Cause)
			}
		}
	})
}

// L9: a stale branch head is never merged, whatever else holds.
func TestPropStaleNeverMerged(t *testing.T) {
	propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
		if !h.haveObs || h.obs.BranchHead == h.req.ExpectBranchHead {
			return
		}
		if countOf[RunMerge](cmds) != 0 || countOf[Predict](cmds) != 0 || h.runMerges != 0 {
			t.Errorf("stale head %q went on to merge", h.obs.BranchHead)
		}
		switch after.(type) {
		case MergePredicting, MergeMerging, Merged:
			t.Errorf("stale head reached %T", after)
		}
	})
}

// The generator reaches every state, command and refusal; without this the
// properties could pass without ever seeing a merge.
func TestMergeGeneratorCoverage(t *testing.T) {
	seen := map[string]bool{}
	for seed := int64(1); seed <= propRuns; seed++ {
		runMergeProp(t, rand.New(rand.NewSource(seed)), func(*testing.T, *mhist, MergeState, MergeInput, MergeState, []MergeCommand) {}, seen)
	}
	want := []string{
		"landing.MergeChecking", "landing.MergePredicting", "landing.MergeMerging", "landing.MergeAborting", "landing.MergeSettling",
		"landing.Merged", "landing.AlreadyMerged", "landing.MergeRefused", "landing.Conflicted", "landing.MergeFailed", "landing.NeedsHuman",
		"landing.Observe", "landing.Predict", "landing.RunMerge", "landing.Abort", "landing.MarkWorktreeMerged", "landing.WriteAudit",
		"refused:untracked_repo", "refused:merge_in_progress", "refused:wrong_branch", "refused:main_dirty", "refused:stale",
		"refused:branch_missing", "refused:worktree_dirty", "refused:worktree_branch",
		"failed:exec", "failed:identity_missing", "failed:interrupted",
		"aborting:landing.AfterConflict", "aborting:landing.AfterFailure",
		"settling:landing.AfterConflict", "settling:landing.AfterFailure",
		"needs_human_from:landing.MergeAborting", "needs_human_from:landing.MergeSettling",
		"conflicted_from:landing.MergePredicting", "conflicted_from:landing.MergeSettling",
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("the generator never reached %s", w)
		}
	}
}
