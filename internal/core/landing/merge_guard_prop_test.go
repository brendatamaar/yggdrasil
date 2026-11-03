package landing

import "testing"

// One named property per guard of the merge table: RunMerge is emitted only
// when that single guard held in the observation it came from. TestProp
// MergeOnlyAfterAllChecks states the conjunction; these name each clause, so a
// dropped guard fails under its own name.
func TestPropRunMergeGuards(t *testing.T) {
	guards := []struct {
		name string
		ok   func(o Observed, req MergeReq) bool
	}{
		{"repo is tracked", func(o Observed, _ MergeReq) bool { return o.Tracked }},
		{"worktree branch is the proposed branch", func(o Observed, r MergeReq) bool { return o.WorktreeBranch == r.Branch }},
		{"branch is not already merged", func(o Observed, _ MergeReq) bool { return !o.AlreadyMerged }},
		{"no merge in progress", func(o Observed, _ MergeReq) bool { return !o.MergeInProgress }},
		{"main is on the target branch", func(o Observed, r MergeReq) bool { return !o.DetachedHead && o.CurrentBranch == r.TargetBranch }},
		{"main checkout is clean", func(o Observed, _ MergeReq) bool { return o.Clean }},
		{"branch exists", func(o Observed, _ MergeReq) bool { return o.BranchHead != "" }},
		{"branch head is the confirmed head", func(o Observed, r MergeReq) bool { return o.BranchHead == r.ExpectBranchHead }},
		{"worktree is clean", func(o Observed, _ MergeReq) bool { return o.WorktreeClean }},
	}
	for _, g := range guards {
		t.Run(g.name, func(t *testing.T) {
			ran := false
			propMerge(t, func(t *testing.T, h *mhist, before MergeState, in MergeInput, after MergeState, cmds []MergeCommand) {
				if countOf[RunMerge](cmds) == 0 {
					return
				}
				ran = true
				if !h.haveObs || !g.ok(h.obs, h.req) {
					t.Errorf("RunMerge although %q did not hold: %+v", g.name, h.obs)
				}
			})
			if !ran {
				t.Error("the generator never produced a RunMerge, so this property checked nothing")
			}
		})
	}
}
