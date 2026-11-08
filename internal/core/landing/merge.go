package landing

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxTitleRunes caps the title used in the merge commit message.
const MaxTitleRunes = 200

// RelPath is a repo-relative file name reported by git.
type RelPath string

// Mode says whether a merge run is fresh or recovers an interrupted one.
type Mode string

const (
	ModeFresh      Mode = "fresh"
	ModeRecovering Mode = "recovering"
)

// Validate reports whether m is known.
func (m Mode) Validate() error {
	switch m {
	case ModeFresh, ModeRecovering:
		return nil
	}
	return fmt.Errorf("unknown merge mode %q", string(m))
}

// MergeReq comes from the confirmed action, validated at the boundary. Path is
// the main checkout, derived by the shell from the repo row, never from a body.
type MergeReq struct {
	Repo             core.RepoID
	Worktree         core.WorktreeID
	Branch           string
	ExpectBranchHead string
	TargetBranch     string
	Title            string
	Path             string
}

// Validate checks the IDs, the hex head, non-empty names and the title.
func (r MergeReq) Validate() error {
	if !isHexOID(r.ExpectBranchHead) {
		return errors.New("the expected branch head must be 40 or 64 lowercase hex characters")
	}
	if r.Branch == "" || r.TargetBranch == "" || r.Path == "" {
		return errors.New("a merge needs a branch, a target branch and a path")
	}
	if n := len([]rune(r.Title)); n == 0 || n > MaxTitleRunes {
		return fmt.Errorf("the merge title must have 1 to %d characters", MaxTitleRunes)
	}
	return errors.Join(r.Repo.Validate(), r.Worktree.Validate())
}

func isHexOID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

// RefuseReason says why a merge was refused before anything was touched.
type RefuseReason string

const (
	RefuseUntrackedRepo   RefuseReason = "untracked_repo"
	RefuseMergeInProgress RefuseReason = "merge_in_progress"
	RefuseWrongBranch     RefuseReason = "wrong_branch"
	RefuseMainDirty       RefuseReason = "main_dirty"
	RefuseStale           RefuseReason = "stale"
	RefuseBranchMissing   RefuseReason = "branch_missing"
	RefuseWorktreeDirty   RefuseReason = "worktree_dirty"
	RefuseWorktreeBranch  RefuseReason = "worktree_branch"
)

// Validate reports whether r is known.
func (r RefuseReason) Validate() error {
	switch r {
	case RefuseUntrackedRepo, RefuseMergeInProgress, RefuseWrongBranch, RefuseMainDirty,
		RefuseStale, RefuseBranchMissing, RefuseWorktreeDirty, RefuseWorktreeBranch:
		return nil
	}
	return fmt.Errorf("unknown refuse reason %q", string(r))
}

// FailReason says why a merge failed.
type FailReason string

const (
	FailExec            FailReason = "exec"
	FailIdentityMissing FailReason = "identity_missing"
	FailInterrupted     FailReason = "interrupted"
)

// Validate reports whether r is known.
func (r FailReason) Validate() error {
	switch r {
	case FailExec, FailIdentityMissing, FailInterrupted:
		return nil
	}
	return fmt.Errorf("unknown fail reason %q", string(r))
}

// Settle says what a run was doing when it looked at the tree once more
// (sealed): AfterConflict or AfterFailure.
type Settle interface{ isSettle() }

// AfterConflict: the merge conflicted and was aborted.
type AfterConflict struct{ Files []RelPath }

// AfterFailure: the merge failed for another reason.
type AfterFailure struct{ Reason FailReason }

func (AfterConflict) isSettle() {}
func (AfterFailure) isSettle()  {}

// MergeState is the state of one merge run (sealed).
type MergeState interface{ isMergeState() }

// MergeIdle: not started.
type MergeIdle struct{}

// MergeChecking: waiting for Observed.
type MergeChecking struct {
	Req  MergeReq
	Mode Mode
}

// MergePredicting: waiting for the conflict prediction.
type MergePredicting struct{ Req MergeReq }

// MergeMerging: RunMerge was emitted; the main checkout may be changing.
type MergeMerging struct{ Req MergeReq }

// MergeAborting: Abort was emitted, after a conflict (Cause AfterConflict) or
// to undo our own interrupted merge (Cause AfterFailure{interrupted}).
type MergeAborting struct {
	Req   MergeReq
	Cause Settle
}

// MergeSettling: looking at the tree once more before deciding.
type MergeSettling struct {
	Req   MergeReq
	Cause Settle
}

// Merged: terminal; the new merge commit.
type Merged struct{ Commit string }

// AlreadyMerged: terminal; the branch was already in the target.
type AlreadyMerged struct{ Head string }

// MergeRefused: terminal; nothing was touched.
type MergeRefused struct{ Reason RefuseReason }

// Conflicted: terminal; the tree is untouched or restored.
type Conflicted struct{ Files []RelPath }

// MergeFailed: terminal.
type MergeFailed struct {
	Reason    FailReason
	Retryable bool
}

// NeedsHuman: terminal; the tree is not clean after a failed merge or abort.
type NeedsHuman struct{ Why string }

func (MergeIdle) isMergeState()       {}
func (MergeChecking) isMergeState()   {}
func (MergePredicting) isMergeState() {}
func (MergeMerging) isMergeState()    {}
func (MergeAborting) isMergeState()   {}
func (MergeSettling) isMergeState()   {}
func (Merged) isMergeState()          {}
func (AlreadyMerged) isMergeState()   {}
func (MergeRefused) isMergeState()    {}
func (Conflicted) isMergeState()      {}
func (MergeFailed) isMergeState()     {}
func (NeedsHuman) isMergeState()      {}

// MergeInput is one event for StepMerge (sealed).
type MergeInput interface{ isMergeInput() }

// MergeStart begins a run.
type MergeStart struct {
	Req  MergeReq
	Mode Mode
	Now  time.Time
}

// Observed is the result of Observe: facts read from git and the row.
type Observed struct {
	Tracked         bool
	Clean           bool
	DetachedHead    bool
	CurrentBranch   string
	Head            string
	BranchHead      string
	MergeInProgress bool
	MergeHead       string
	// AlreadyMerged: the branch head is an ancestor of Head.
	AlreadyMerged bool
	WorktreeClean bool
	// WorktreeBranch is the branch the worktree row names; it must be the
	// proposed branch.
	WorktreeBranch string
	Now            time.Time
}

// PredictResult is the outcome of the conflict prediction (sealed).
type PredictResult interface{ isPredictResult() }

// PredictClean: no conflicts.
type PredictClean struct{}

// PredictConflicts: the merge would conflict in these files.
type PredictConflicts struct{ Files []RelPath }

// PredictUnsupported: git is older than 2.38.
type PredictUnsupported struct{}

// PredictError: the prediction failed.
type PredictError struct{ Msg string }

func (PredictClean) isPredictResult()       {}
func (PredictConflicts) isPredictResult()   {}
func (PredictUnsupported) isPredictResult() {}
func (PredictError) isPredictResult()       {}

// Predicted is the result of Predict.
type Predicted struct {
	Result PredictResult
	Now    time.Time
}

// MergeDone: RunMerge made a merge commit.
type MergeDone struct {
	Commit string
	Now    time.Time
}

// MergeConflicted: RunMerge hit conflicts.
type MergeConflicted struct {
	Files []RelPath
	Now   time.Time
}

// MergeErrored: RunMerge failed for another reason.
type MergeErrored struct {
	Reason FailReason
	Msg    string
	Now    time.Time
}

// AbortDone: the abort succeeded (or nothing was in progress).
type AbortDone struct{ Now time.Time }

// AbortErrored: the abort failed.
type AbortErrored struct {
	Msg string
	Now time.Time
}

func (MergeStart) isMergeInput()      {}
func (Observed) isMergeInput()        {}
func (Predicted) isMergeInput()       {}
func (MergeDone) isMergeInput()       {}
func (MergeConflicted) isMergeInput() {}
func (MergeErrored) isMergeInput()    {}
func (AbortDone) isMergeInput()       {}
func (AbortErrored) isMergeInput()    {}

// MergeCommand is plain data that asks the shell for an effect (sealed). The
// set is closed on purpose (L3): no push, reset, branch or delete command.
type MergeCommand interface{ isMergeCommand() }

// Observe reads the facts for Observed. Read only.
type Observe struct{ Req MergeReq }

// Predict runs the conflict prediction (writes objects only).
type Predict struct{ Req MergeReq }

// RunMerge runs git merge --no-ff of ExpectBranchHead with Message. NOT
// idempotent; at most one per run (L2).
type RunMerge struct {
	Req     MergeReq
	Message string
}

// Abort runs git merge --abort in the main checkout. Idempotent.
type Abort struct{ Req MergeReq }

// MarkWorktreeMerged moves the worktree row active -> merged. Idempotent.
type MarkWorktreeMerged struct{ Worktree core.WorktreeID }

// WriteAudit records a terminal outcome in the proposal's transaction.
type WriteAudit struct {
	Action  string
	Outcome string
}

func (Observe) isMergeCommand()            {}
func (Predict) isMergeCommand()            {}
func (RunMerge) isMergeCommand()           {}
func (Abort) isMergeCommand()              {}
func (MarkWorktreeMerged) isMergeCommand() {}
func (WriteAudit) isMergeCommand()         {}
