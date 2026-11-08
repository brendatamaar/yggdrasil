package landing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var (
	req    = mergeReq
	goodOb = Observed{Tracked: true, Clean: true, CurrentBranch: "main", Head: hx(3), BranchHead: hx(1), WorktreeClean: true, WorktreeBranch: "ygg/t1"}
	ownOb  = Observed{Tracked: true, Clean: false, MergeInProgress: true, MergeHead: hx(1), CurrentBranch: "main", BranchHead: hx(1), Head: hx(3), WorktreeClean: true, WorktreeBranch: "ygg/t1"}
)

func aud(outcome string) WriteAudit { return WriteAudit{Action: "landing.merge", Outcome: outcome} }

func with(o Observed, f func(*Observed)) Observed { f(&o); return o }

func TestStepMergeTable(t *testing.T) {
	files := []RelPath{"a.go"}
	msg := "Merge branch 'ygg/t1' (Add cart)"
	rows := []struct {
		name string
		from MergeState
		in   MergeInput
		want MergeState
		cmds []MergeCommand
	}{
		{"idle start fresh", MergeIdle{}, MergeStart{Req: req, Mode: ModeFresh}, MergeChecking{Req: req, Mode: ModeFresh}, []MergeCommand{Observe{Req: req}}},
		{"idle start recovering", MergeIdle{}, MergeStart{Req: req, Mode: ModeRecovering}, MergeChecking{Req: req, Mode: ModeRecovering}, []MergeCommand{Observe{Req: req}}},
		{"idle start bad request", MergeIdle{}, MergeStart{Req: MergeReq{}, Mode: ModeFresh}, MergeFailed{Reason: FailExec}, []MergeCommand{aud("failed_exec")}},
		{"idle start bad mode", MergeIdle{}, MergeStart{Req: req, Mode: "x"}, MergeFailed{Reason: FailExec}, []MergeCommand{aud("failed_exec")}},
		{"idle ignores observed", MergeIdle{}, goodOb, MergeIdle{}, nil},

		{"untracked", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.Tracked = false }), MergeRefused{RefuseUntrackedRepo}, []MergeCommand{aud("refused_untracked_repo")}},
		{"untracked beats already merged", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.Tracked, o.AlreadyMerged = false, true }), MergeRefused{RefuseUntrackedRepo}, []MergeCommand{aud("refused_untracked_repo")}},
		{"already merged", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.AlreadyMerged = true }), AlreadyMerged{Head: hx(3)}, []MergeCommand{MarkWorktreeMerged{Worktree: "w1"}, aud("already_merged")}},
		{"already merged beats dirty", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.AlreadyMerged, o.Clean = true, false }), AlreadyMerged{Head: hx(3)}, []MergeCommand{MarkWorktreeMerged{Worktree: "w1"}, aud("already_merged")}},
		{"recovering own merge", MergeChecking{Req: req, Mode: ModeRecovering}, ownOb, MergeAborting{Req: req, Cause: AfterFailure{FailInterrupted}}, []MergeCommand{Abort{Req: req}}},
		{"fresh never aborts own merge", MergeChecking{Req: req, Mode: ModeFresh}, ownOb, MergeRefused{RefuseMergeInProgress}, []MergeCommand{aud("refused_merge_in_progress")}},
		{"recovering other merge", MergeChecking{Req: req, Mode: ModeRecovering}, with(ownOb, func(o *Observed) { o.MergeHead = hx(5) }), MergeRefused{RefuseMergeInProgress}, []MergeCommand{aud("refused_merge_in_progress")}},
		{"recovering empty merge head", MergeChecking{Req: req, Mode: ModeRecovering}, with(ownOb, func(o *Observed) { o.MergeHead, o.BranchHead = "", "" }), MergeRefused{RefuseMergeInProgress}, []MergeCommand{aud("refused_merge_in_progress")}},
		{"recovering branch moved on", MergeChecking{Req: req, Mode: ModeRecovering}, with(ownOb, func(o *Observed) { o.MergeHead, o.BranchHead = hx(2), hx(2) }), MergeRefused{RefuseMergeInProgress}, []MergeCommand{aud("refused_merge_in_progress")}},
		{"merge in progress", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.MergeInProgress = true }), MergeRefused{RefuseMergeInProgress}, []MergeCommand{aud("refused_merge_in_progress")}},
		{"detached", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.DetachedHead = true }), MergeRefused{RefuseWrongBranch}, []MergeCommand{aud("refused_wrong_branch")}},
		{"other branch", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.CurrentBranch = "dev" }), MergeRefused{RefuseWrongBranch}, []MergeCommand{aud("refused_wrong_branch")}},
		{"main dirty", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.Clean = false }), MergeRefused{RefuseMainDirty}, []MergeCommand{aud("refused_main_dirty")}},
		{"branch missing", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.BranchHead = "" }), MergeRefused{RefuseBranchMissing}, []MergeCommand{aud("refused_branch_missing")}},
		{"stale", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.BranchHead = hx(2) }), MergeRefused{RefuseStale}, []MergeCommand{aud("refused_stale")}},
		{"worktree dirty", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.WorktreeClean = false }), MergeRefused{RefuseWorktreeDirty}, []MergeCommand{aud("refused_worktree_dirty")}},
		{"worktree on another branch", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.WorktreeBranch = "ygg/other" }), MergeRefused{RefuseWorktreeBranch}, []MergeCommand{aud("refused_worktree_branch")}},
		{"worktree branch beats already merged", MergeChecking{Req: req}, with(goodOb, func(o *Observed) { o.WorktreeBranch, o.AlreadyMerged = "", true }), MergeRefused{RefuseWorktreeBranch}, []MergeCommand{aud("refused_worktree_branch")}},
		{"all pass", MergeChecking{Req: req}, goodOb, MergePredicting{Req: req}, []MergeCommand{Predict{Req: req}}},
		{"checking ignores predicted", MergeChecking{Req: req}, Predicted{Result: PredictClean{}}, MergeChecking{Req: req}, nil},

		{"predict clean", MergePredicting{Req: req}, Predicted{Result: PredictClean{}}, MergeMerging{Req: req}, []MergeCommand{RunMerge{Req: req, Message: msg}}},
		{"predict unsupported", MergePredicting{Req: req}, Predicted{Result: PredictUnsupported{}}, MergeMerging{Req: req}, []MergeCommand{RunMerge{Req: req, Message: msg}}},
		{"predict conflicts", MergePredicting{Req: req}, Predicted{Result: PredictConflicts{Files: files}}, Conflicted{Files: files}, []MergeCommand{aud("conflicted")}},
		{"predict error", MergePredicting{Req: req}, Predicted{Result: PredictError{Msg: "x"}}, MergeFailed{Reason: FailExec, Retryable: true}, []MergeCommand{aud("failed_exec")}},
		{"predict nil result", MergePredicting{Req: req}, Predicted{}, MergePredicting{Req: req}, nil},

		{"merge done", MergeMerging{Req: req}, MergeDone{Commit: hx(9)}, Merged{Commit: hx(9)}, []MergeCommand{MarkWorktreeMerged{Worktree: "w1"}, aud("merged")}},
		{"merge conflicted", MergeMerging{Req: req}, MergeConflicted{Files: files}, MergeAborting{Req: req, Cause: AfterConflict{Files: files}}, []MergeCommand{Abort{Req: req}}},
		{"merge errored identity", MergeMerging{Req: req}, MergeErrored{Reason: FailIdentityMissing}, MergeSettling{Req: req, Cause: AfterFailure{FailIdentityMissing}}, []MergeCommand{Observe{Req: req}}},
		{"merge errored exec", MergeMerging{Req: req}, MergeErrored{Reason: FailExec}, MergeSettling{Req: req, Cause: AfterFailure{FailExec}}, []MergeCommand{Observe{Req: req}}},
		{"merge errored odd reason", MergeMerging{Req: req}, MergeErrored{Reason: "weird"}, MergeSettling{Req: req, Cause: AfterFailure{FailExec}}, []MergeCommand{Observe{Req: req}}},
		{"merging ignores observed", MergeMerging{Req: req}, goodOb, MergeMerging{Req: req}, nil},

		{"abort done after conflict", MergeAborting{Req: req, Cause: AfterConflict{Files: files}}, AbortDone{}, MergeSettling{Req: req, Cause: AfterConflict{Files: files}}, []MergeCommand{Observe{Req: req}}},
		{"abort done recovery", MergeAborting{Req: req, Cause: AfterFailure{FailInterrupted}}, AbortDone{}, MergeSettling{Req: req, Cause: AfterFailure{FailInterrupted}}, []MergeCommand{Observe{Req: req}}},
		{"abort errored", MergeAborting{Req: req, Cause: AfterConflict{}}, AbortErrored{Msg: "x"}, NeedsHuman{Why: "The merge could not be undone. Run `git merge --abort` in " + req.Path + " and check the folder before trying again."}, []MergeCommand{aud("needs_human")}},

		{"settle conflict clean", MergeSettling{Req: req, Cause: AfterConflict{Files: files}}, with(goodOb, func(o *Observed) {}), Conflicted{Files: files}, []MergeCommand{aud("conflicted")}},
		{"settle failure clean", MergeSettling{Req: req, Cause: AfterFailure{FailIdentityMissing}}, goodOb, MergeFailed{Reason: FailIdentityMissing, Retryable: true}, []MergeCommand{aud("failed_identity_missing")}},
		{"settle interrupted clean", MergeSettling{Req: req, Cause: AfterFailure{FailInterrupted}}, goodOb, MergeFailed{Reason: FailInterrupted, Retryable: true}, []MergeCommand{aud("failed_interrupted")}},
		{"settle dirty", MergeSettling{Req: req, Cause: AfterConflict{Files: files}}, with(goodOb, func(o *Observed) { o.Clean = false }), NeedsHuman{}, []MergeCommand{aud("needs_human")}},
		{"settle merge in progress", MergeSettling{Req: req, Cause: AfterFailure{FailExec}}, with(goodOb, func(o *Observed) { o.MergeInProgress = true }), NeedsHuman{}, []MergeCommand{aud("needs_human")}},
		{"settle ignores predicted", MergeSettling{Req: req, Cause: AfterFailure{FailExec}}, Predicted{}, MergeSettling{Req: req, Cause: AfterFailure{FailExec}}, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, cmds := StepMerge(r.from, r.in)
			if nh, ok := r.want.(NeedsHuman); ok && nh.Why == "" {
				g, ok := got.(NeedsHuman)
				if !ok || strings.TrimSpace(g.Why) == "" || !strings.Contains(g.Why, req.Path) {
					t.Fatalf("got %#v, want NeedsHuman with a message naming the folder", got)
				}
				got = r.want
			}
			if !reflect.DeepEqual(got, r.want) {
				t.Errorf("state = %#v, want %#v", got, r.want)
			}
			if !reflect.DeepEqual(cmds, r.cmds) {
				t.Errorf("commands = %#v, want %#v", cmds, r.cmds)
			}
		})
	}
}

func TestStepMergeTerminalsIgnoreEverything(t *testing.T) {
	terms := []MergeState{Merged{}, AlreadyMerged{}, MergeRefused{RefuseStale}, Conflicted{}, MergeFailed{}, NeedsHuman{}}
	ins := []MergeInput{MergeStart{Req: req}, goodOb, Predicted{Result: PredictClean{}}, MergeDone{}, MergeConflicted{}, MergeErrored{}, AbortDone{}, AbortErrored{}}
	for _, s := range terms {
		for _, in := range ins {
			if got, cmds := StepMerge(s, in); !reflect.DeepEqual(got, s) || cmds != nil {
				t.Errorf("%T on %T changed to %T", s, in, got)
			}
		}
	}
	if got, cmds := StepMerge(nil, goodOb); got != nil || cmds != nil {
		t.Error("nil state changed")
	}
}

func TestStepMergeDoesNotAliasInputFiles(t *testing.T) {
	files := []RelPath{"a"}
	s, _ := StepMerge(MergeMerging{Req: req}, MergeConflicted{Files: files})
	files[0] = "changed"
	if s.(MergeAborting).Cause.(AfterConflict).Files[0] != "a" {
		t.Error("state shares the caller's slice")
	}
}

// ---- merge message ----

func TestMergeMessage(t *testing.T) {
	cases := []struct{ branch, title, want string }{
		{"ygg/t1", "Add cart", "Merge branch 'ygg/t1' (Add cart)"},
		{"b", "line1\nline2\r\n\nSigned-off-by: x", "Merge branch 'b' (line1 line2 Signed-off-by: x)"},
		{"b", "esc\x1b[31mred\x00", "Merge branch 'b' (esc [31mred)"},
		{"b", "\u202eevil\u200b", "Merge branch 'b' (evil)"},
		{"b", " \n\t ", "Merge branch 'b'"},
		{"b\nc", "t", "Merge branch 'b c' (t)"},
		{"b", "tab\tsep", "Merge branch 'b' (tab sep)"},
	}
	for _, c := range cases {
		if got := MergeMessage(c.branch, c.title); got != c.want {
			t.Errorf("MergeMessage(%q,%q) = %q, want %q", c.branch, c.title, got, c.want)
		}
	}
	got := MergeMessage("b", strings.Repeat("é", 1000))
	if n := utf8.RuneCountInString(got); n != len("Merge branch 'b' ()")+MaxTitleRunes {
		t.Errorf("title not capped: %d runes", n)
	}
	if n := utf8.RuneCountInString(MergeMessage(strings.Repeat("b", 1000), "t")); n > maxBranchRunes+60 {
		t.Errorf("branch not capped: %d", n)
	}
}

func FuzzMergeMessage(f *testing.F) {
	f.Add("ygg/t1", "Add cart")
	f.Add("b\nc", "x\x1b[0m\r\ny")
	f.Fuzz(func(t *testing.T, branch, title string) {
		m := MergeMessage(branch, title)
		if !strings.HasPrefix(m, "Merge branch '") {
			t.Fatalf("bad prefix %q", m)
		}
		for _, r := range m {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Fatalf("control character in %q", m)
			}
		}
		if utf8.RuneCountInString(m) > maxBranchRunes+MaxTitleRunes+30 {
			t.Fatalf("message too long: %d", utf8.RuneCountInString(m))
		}
		if strings.Contains(m, "\n") {
			t.Fatal("newline")
		}
	})
}

// ---- closed command sets ----

func TestCommandTypesAreExactlyTheClosedSets(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || (fd.Name.Name != "isMergeCommand" && fd.Name.Name != "isRemoveCommand") {
				continue
			}
			found[fd.Name.Name] = append(found[fd.Name.Name], fd.Recv.List[0].Type.(*ast.Ident).Name)
		}
	}
	for k := range found {
		slices.Sort(found[k])
	}
	wantMerge := []string{"Abort", "MarkWorktreeMerged", "Observe", "Predict", "RunMerge", "WriteAudit"}
	wantRemove := []string{"DeleteStaged", "MarkRemoved", "MarkRemoving", "MoveToStaging", "ObserveWorktree", "PruneWorktrees", "WriteAudit"}
	if !slices.Equal(found["isMergeCommand"], wantMerge) {
		t.Errorf("merge commands = %v, want %v", found["isMergeCommand"], wantMerge)
	}
	if !slices.Equal(found["isRemoveCommand"], wantRemove) {
		t.Errorf("remove commands = %v, want %v", found["isRemoveCommand"], wantRemove)
	}
}

// ---- removal ----

func raud(outcome string) WriteAudit { return WriteAudit{Action: "landing.remove", Outcome: outcome} }

var (
	rreq    = removeReq
	planed  = StagePath{Root: `D:\wt`, Leaf: "shop_api-ab12-st9"}
	oldSt   = StagePath{Root: `D:\wt`, Leaf: "shop_api-ab12-old1"}
	rreqOld = func() RemoveReq { r := removeReq; r.StagedLeaf = oldSt.Leaf; return r }()
	rGood   = RemoveObserved{UnderRoot: true, RowStatus: core.WorktreeMerged, PathExists: true, Clean: true, StatusReadable: true}
)

func rwith(o RemoveObserved, f func(*RemoveObserved)) RemoveObserved { f(&o); return o }

func TestStepRemoveTable(t *testing.T) {
	chk := RemoveChecking{Req: rreq, Planned: planed}
	chkOld := RemoveChecking{Req: rreqOld, Planned: planed}
	refused := func(r RemoveRefuse) RemoveState { return RemoveRefused{Reason: r} }
	rows := []struct {
		name string
		from RemoveState
		in   RemoveInput
		want RemoveState
		cmds []RemoveCommand
	}{
		{"start", RemoveIdle{}, RemoveStart{Req: rreq, StageID: "st9"}, chk, []RemoveCommand{ObserveWorktree{Req: rreq}}},
		{"start bad id", RemoveIdle{}, RemoveStart{Req: rreq, StageID: "a/b"}, RemoveFailed{Reason: "The removal request was not valid. Nothing was changed."}, []RemoveCommand{raud("failed")}},
		{"start bad request", RemoveIdle{}, RemoveStart{StageID: "st9"}, RemoveFailed{Reason: "The removal request was not valid. Nothing was changed."}, []RemoveCommand{raud("failed")}},
		{"idle ignores observed", RemoveIdle{}, rGood, RemoveIdle{}, nil},

		{"outside root", chk, rwith(rGood, func(o *RemoveObserved) { o.UnderRoot = false }), refused(RemoveOutsideRoot), []RemoveCommand{raud("refused_outside_root")}},
		{"outside root beats main", chk, rwith(rGood, func(o *RemoveObserved) { o.UnderRoot, o.IsMain = false, true }), refused(RemoveOutsideRoot), []RemoveCommand{raud("refused_outside_root")}},
		{"main checkout", chk, rwith(rGood, func(o *RemoveObserved) { o.IsMain = true }), refused(RemoveMainCheckout), []RemoveCommand{raud("refused_main_checkout")}},
		{"already removed", chk, rwith(rGood, func(o *RemoveObserved) { o.RowStatus, o.PathExists = core.WorktreeRemoved, false }), Removed{Note: noteAlreadyRemoved}, []RemoveCommand{raud("already_removed")}},
		{"crash after delete, row removing", chk, rwith(rGood, func(o *RemoveObserved) { o.RowStatus, o.PathExists = core.WorktreeRemoving, false }), Removed{Note: noteAlreadyRemoved}, []RemoveCommand{MarkRemoved{Worktree: "w1"}, raud("already_removed")}},
		{"row removed but path exists", chk, rwith(rGood, func(o *RemoveObserved) { o.RowStatus = core.WorktreeRemoved }), Moving{Req: rreq, Staged: planed}, []RemoveCommand{MoveToStaging{Req: rreq, To: planed}}},
		{"resume staged, row removing", chkOld, rwith(rGood, func(o *RemoveObserved) {
			o.RowStatus, o.PathExists, o.StagedExists, o.Staged = core.WorktreeRemoving, false, true, oldSt
		}), Deleting{Req: rreqOld, Staged: oldSt}, []RemoveCommand{DeleteStaged{Staged: oldSt}}},
		{"resume staged, crash before MarkRemoving", chkOld, rwith(rGood, func(o *RemoveObserved) {
			o.PathExists, o.StagedExists, o.Staged, o.OtherPrunable = false, true, oldSt, true
		}), Deleting{Req: rreqOld, Staged: oldSt, OtherPrunable: true}, []RemoveCommand{MarkRemoving{Worktree: "w1", Staged: oldSt}, DeleteStaged{Staged: oldSt}}},
		{"resume beats dirty and task", chkOld, rwith(rGood, func(o *RemoveObserved) {
			o.StagedExists, o.Staged, o.Clean, o.TaskLive, o.RowStatus, o.PathExists = true, oldSt, false, true, core.WorktreeRemoving, false
		}), Deleting{Req: rreqOld, Staged: oldSt}, []RemoveCommand{DeleteStaged{Staged: oldSt}}},
		{"staged of another worktree", chk, rwith(rGood, func(o *RemoveObserved) {
			o.StagedExists, o.Staged, o.PathExists = true, StagePath{Root: `D:\wt`, Leaf: "other-1"}, false
		}), RemoveFailed{Reason: "A folder from an earlier removal could not be matched to this worktree, so nothing was deleted. Check the .removing folder in the worktree root."}, []RemoveCommand{raud("failed")}},
		{"staged under another root", chk, rwith(rGood, func(o *RemoveObserved) {
			o.StagedExists, o.Staged, o.PathExists = true, StagePath{Root: `E:\x`, Leaf: "shop_api-ab12-1"}, false
		}), RemoveFailed{Reason: "A folder from an earlier removal could not be matched to this worktree, so nothing was deleted. Check the .removing folder in the worktree root."}, []RemoveCommand{raud("failed")}},
		{"unreadable", chk, rwith(rGood, func(o *RemoveObserved) { o.StatusReadable = false }), refused(RemoveUnreadable), []RemoveCommand{raud("refused_unreadable")}},
		{"task running", chk, rwith(rGood, func(o *RemoveObserved) { o.TaskLive = true }), refused(RemoveTaskRunning), []RemoveCommand{raud("refused_task_running")}},
		{"build live", chk, rwith(rGood, func(o *RemoveObserved) { o.BuildLive = true }), refused(RemoveInUse), []RemoveCommand{raud("refused_in_use")}},
		{"dirty", chk, rwith(rGood, func(o *RemoveObserved) { o.Clean = false }), refused(RemoveDirty), []RemoveCommand{raud("refused_dirty")}},
		{"all pass", chk, rGood, Moving{Req: rreq, Staged: planed}, []RemoveCommand{MoveToStaging{Req: rreq, To: planed}}},
		{"all pass, others prunable", chk, rwith(rGood, func(o *RemoveObserved) { o.OtherPrunable = true }), Moving{Req: rreq, Staged: planed, OtherPrunable: true}, []RemoveCommand{MoveToStaging{Req: rreq, To: planed}}},

		{"moved", Moving{Req: rreq, Staged: planed}, Moved{}, Deleting{Req: rreq, Staged: planed}, []RemoveCommand{MarkRemoving{Worktree: "w1", Staged: planed}, DeleteStaged{Staged: planed}}},
		{"move failed", Moving{Req: rreq, Staged: planed}, MoveFailed{Class: LockSharing, Holders: []string{"Code.exe", "code.exe"}}, Locked{Class: LockSharing, Holders: []string{"Code.exe"}}, []RemoveCommand{raud("locked")}},
		{"move failed unknown class", Moving{Req: rreq, Staged: planed}, MoveFailed{Class: ""}, Locked{Class: LockOther}, []RemoveCommand{raud("locked")}},
		{"moving ignores deleted", Moving{Req: rreq, Staged: planed}, Deleted{}, Moving{Req: rreq, Staged: planed}, nil},

		{"deleted, prune", Deleting{Req: rreq, Staged: planed}, Deleted{}, Pruning{Req: rreq}, []RemoveCommand{PruneWorktrees{Repo: "r1"}}},
		{"deleted, others prunable", Deleting{Req: rreq, Staged: planed, OtherPrunable: true}, Deleted{}, Removed{Note: noteGitCleanupLater}, []RemoveCommand{MarkRemoved{Worktree: "w1"}, raud("removed_unpruned")}},
		{"delete failed", Deleting{Req: rreq, Staged: planed}, DeleteFailed{Class: LockAccessDenied, Holders: []string{"a", "b"}, Remaining: 3}, Stuck{Staged: planed, Class: LockAccessDenied, Holders: []string{"a", "b"}, Remaining: 3}, []RemoveCommand{raud("stuck")}},
		{"delete failed negative remaining", Deleting{Req: rreq, Staged: planed}, DeleteFailed{Class: LockOther, Remaining: -4}, Stuck{Staged: planed, Class: LockOther}, []RemoveCommand{raud("stuck")}},
		{"deleting ignores moved", Deleting{Req: rreq, Staged: planed}, Moved{}, Deleting{Req: rreq, Staged: planed}, nil},

		{"pruned", Pruning{Req: rreq}, Pruned{}, Removed{}, []RemoveCommand{MarkRemoved{Worktree: "w1"}, raud("removed")}},
		{"prune failed", Pruning{Req: rreq}, PruneFailed{Msg: "x"}, Removed{Note: noteGitCleanupLater}, []RemoveCommand{MarkRemoved{Worktree: "w1"}, raud("removed_unpruned")}},
		{"pruning ignores deleted", Pruning{Req: rreq}, Deleted{}, Pruning{Req: rreq}, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, cmds := StepRemove(r.from, r.in)
			if !reflect.DeepEqual(got, r.want) {
				t.Errorf("state = %#v, want %#v", got, r.want)
			}
			if !reflect.DeepEqual(cmds, r.cmds) {
				t.Errorf("commands = %#v, want %#v", cmds, r.cmds)
			}
		})
	}
}

func TestStepRemoveTerminalsIgnoreEverything(t *testing.T) {
	terms := []RemoveState{Removed{}, RemoveRefused{RemoveDirty}, Locked{Class: LockOther}, Stuck{Class: LockOther}, RemoveFailed{}}
	ins := []RemoveInput{RemoveStart{Req: rreq, StageID: "x"}, rGood, Moved{}, MoveFailed{}, Deleted{}, DeleteFailed{}, Pruned{}, PruneFailed{}}
	for _, s := range terms {
		for _, in := range ins {
			if got, cmds := StepRemove(s, in); !reflect.DeepEqual(got, s) || cmds != nil {
				t.Errorf("%T on %T changed to %T", s, in, got)
			}
		}
	}
}

func TestLockedAndStuckMessages(t *testing.T) {
	l := Locked{Class: LockSharing, Holders: []string{"Code.exe", "node.exe"}}
	m := l.Message()
	for _, want := range []string{"may be using", "Nothing was changed", "Close", "retry", "Code.exe, node.exe"} {
		if !strings.Contains(m, want) {
			t.Errorf("message %q lacks %q", m, want)
		}
	}
	if strings.Contains(strings.ToLower(m), "is using") {
		t.Errorf("message claims certainty: %q", m)
	}
	for _, c := range []LockClass{LockSharing, LockAccessDenied, LockPathTooLong, LockOther, ""} {
		if (Locked{Class: c}).Message() == "" || (Stuck{Class: c, Remaining: 2}).Message() == "" {
			t.Errorf("class %q: empty message", c)
		}
	}
	s := Stuck{Class: LockPathTooLong, Remaining: 7}.Message()
	if !strings.Contains(s, "not finished") || !strings.Contains(s, "7") || !strings.Contains(s, "too long") {
		t.Errorf("stuck message %q", s)
	}
	if strings.Contains((Stuck{Class: LockOther}).Message(), "About") {
		t.Error("stuck message mentions a count of zero")
	}
}

func TestCleanHolders(t *testing.T) {
	in := []string{"A.exe", "a.EXE", "  ", "b\x1b[1m.exe", "c.exe", "d.exe", "e.exe", "f.exe", "g.exe", strings.Repeat("z", 200)}
	got := cleanHolders(in)
	want := []string{"A.exe", "b [1m.exe", "c.exe", "d.exe", "e.exe"}
	if !slices.Equal(got, want) {
		t.Errorf("cleanHolders = %q, want %q", got, want)
	}
	long := cleanHolders([]string{strings.Repeat("z", 200)})
	if len(long) != 1 || utf8.RuneCountInString(long[0]) != maxHolderRunes {
		t.Errorf("long name not capped: %q", long)
	}
	if cleanHolders(nil) != nil {
		t.Error("nil holders not nil")
	}
}

// A staged folder is claimed only by exact leaf: the remembered one, else the
// planned one. A prefix match (worktree a vs a-b) never claims.
func TestStagedExactMatch(t *testing.T) {
	a := RemoveReq{Worktree: "w1", Repo: "r1", Path: `D:\wt\a`, Root: `D:\wt`, Name: "a"}
	ab := a
	ab.Name, ab.Path, ab.Worktree = "a-b", `D:\wt\a-b`, "w2"
	planA, _ := StagingPath(a.Root, a.Name, "s1")
	otherLeftover := StagePath{Root: `D:\wt`, Leaf: "a-b-old"} // made by a-b
	obs := RemoveObserved{UnderRoot: true, RowStatus: core.WorktreeActive, StagedExists: true, Staged: otherLeftover, Clean: true, StatusReadable: true}
	got, cmds := StepRemove(RemoveChecking{Req: a, Planned: planA}, obs)
	if _, ok := got.(RemoveFailed); !ok || countOf[DeleteStaged](cmds) != 0 || countOf[MarkRemoving](cmds) != 0 {
		t.Fatalf("a claimed a-b's leftover: %T %v", got, cmds)
	}
	ab.StagedLeaf = otherLeftover.Leaf
	got, _ = StepRemove(RemoveChecking{Req: ab, Planned: planA}, obs)
	if _, ok := got.(Deleting); !ok {
		t.Fatalf("a-b did not resume its own leftover: %T", got)
	}
	bad := a
	bad.StagedLeaf = "../x"
	if bad.Validate() == nil {
		t.Error("a bad remembered leaf was accepted")
	}
}

func TestStagedAndLivePathIsNeverResumed(t *testing.T) {
	chkOld := RemoveChecking{Req: rreqOld, Planned: planed}
	o := rwith(rGood, func(o *RemoveObserved) {
		o.StagedExists, o.Staged, o.PathExists, o.RowStatus = true, oldSt, true, core.WorktreeActive
	})
	got, cmds := StepRemove(chkOld, o)
	if f, ok := got.(RemoveFailed); !ok || f.Retryable || countOf[DeleteStaged](cmds) != 0 || countOf[MarkRemoving](cmds) != 0 {
		t.Fatalf("got %T %v", got, cmds)
	}
}
