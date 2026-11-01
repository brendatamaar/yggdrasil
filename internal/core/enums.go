package core

import "fmt"

// TaskMode is what a task may do (AG-06 to AG-09).
type TaskMode string

const (
	// ModeAsk is read-only.
	ModeAsk TaskMode = "ask"
	// ModeCode edits only inside the task's own worktree(s).
	ModeCode TaskMode = "code"
	// ModeCurrentBranch edits the main checkout directly (opt-in, warned).
	ModeCurrentBranch TaskMode = "current_branch"
	// ModeProject edits all tracked repos of a project.
	ModeProject TaskMode = "project"
)

// TaskScope is how wide a task reaches.
type TaskScope string

const (
	ScopeRepo    TaskScope = "repo"
	ScopeProject TaskScope = "project"
)

// TaskStatus is the lifecycle of a task. The board columns (AG-12) are
// derived from it.
type TaskStatus string

const (
	TaskDraft     TaskStatus = "draft"
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskNeedsYou  TaskStatus = "needs_you"
	TaskReview    TaskStatus = "review"
	TaskDone      TaskStatus = "done"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// WorktreeStatus is the lifecycle of a worktree.
type WorktreeStatus string

const (
	WorktreeActive WorktreeStatus = "active"
	WorktreeMerged WorktreeStatus = "merged"
	// WorktreeRemoving: the folder was moved into the staging folder and its
	// delete is not finished (docs/design/landing.md). Monotone: active,
	// merged, removing, removed.
	WorktreeRemoving WorktreeStatus = "removing"
	WorktreeRemoved  WorktreeStatus = "removed"
)

// ProposalKind is what a proposal would do once confirmed (RV-02, MCP-05).
type ProposalKind string

const (
	ProposalCommit       ProposalKind = "commit"
	ProposalPush         ProposalKind = "push"
	ProposalMergeRequest ProposalKind = "merge_request"
	ProposalInstall      ProposalKind = "install"
	ProposalPublish      ProposalKind = "publish"
	ProposalAgentFile    ProposalKind = "agent_file"
	ProposalDeleteFiles  ProposalKind = "delete_files"
	ProposalSubtasks     ProposalKind = "subtasks"
	// ProposalMergeLocal merges the agent branch into the main checkout with
	// --no-ff and never pushes (RV-05).
	ProposalMergeLocal ProposalKind = "merge_local"
	// ProposalRemoveWorktree removes a clean worktree after confirmation
	// (RV-06).
	ProposalRemoveWorktree ProposalKind = "remove_worktree"
)

// ProposalStatus is the lifecycle of a proposal.
type ProposalStatus string

const (
	ProposalPending   ProposalStatus = "pending"
	ProposalConfirmed ProposalStatus = "confirmed"
	ProposalRejected  ProposalStatus = "rejected"
	ProposalExecuted  ProposalStatus = "executed"
	ProposalFailed    ProposalStatus = "failed"
)

// ChangeSetStatus is the lifecycle of a change set.
type ChangeSetStatus string

const (
	ChangeSetOpen    ChangeSetStatus = "open"
	ChangeSetShipped ChangeSetStatus = "shipped"
	ChangeSetClosed  ChangeSetStatus = "closed"
)

// BuildStatus is the state of a build or one of its steps.
type BuildStatus string

const (
	BuildQueued    BuildStatus = "queued"
	BuildRunning   BuildStatus = "running"
	BuildSucceeded BuildStatus = "succeeded"
	BuildFailed    BuildStatus = "failed"
	BuildCancelled BuildStatus = "cancelled"
	// BuildInterrupted: ygg stopped while the build ran, so its process tree
	// is gone (the job object guarantees it).
	BuildInterrupted BuildStatus = "interrupted"
)

// StepStatus is the state of one build step.
type StepStatus string

const (
	StepQueued    StepStatus = "queued"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepCancelled StepStatus = "cancelled"
	// StepSkipped is for a step that never ran because an earlier one failed
	// or the build was cancelled.
	StepSkipped StepStatus = "skipped"
	// StepInterrupted: ygg stopped while the step ran.
	StepInterrupted StepStatus = "interrupted"
)

// StepOutcome is how the last attempt of a step ended. A step that has not
// finished has no outcome (the empty value, which Validate rejects: check for
// it before validating).
type StepOutcome string

const (
	OutcomeSucceeded   StepOutcome = "succeeded"
	OutcomeFailed      StepOutcome = "failed"
	OutcomeStartFailed StepOutcome = "start_failed"
	OutcomeCancelled   StepOutcome = "cancelled"
	OutcomeInterrupted StepOutcome = "interrupted"
)

// Validate reports whether m is a known mode.
func (m TaskMode) Validate() error {
	switch m {
	case ModeAsk, ModeCode, ModeCurrentBranch, ModeProject:
		return nil
	}
	return fmt.Errorf("unknown task mode %q", string(m))
}

// Validate reports whether s is a known scope.
func (s TaskScope) Validate() error {
	switch s {
	case ScopeRepo, ScopeProject:
		return nil
	}
	return fmt.Errorf("unknown task scope %q", string(s))
}

// Validate reports whether s is a known status.
func (s TaskStatus) Validate() error {
	switch s {
	case TaskDraft, TaskQueued, TaskRunning, TaskNeedsYou, TaskReview, TaskDone, TaskFailed, TaskCancelled:
		return nil
	}
	return fmt.Errorf("unknown task status %q", string(s))
}

// Terminal reports whether no further work happens in this status.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskDone, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// Validate reports whether k is a known proposal kind.
func (k ProposalKind) Validate() error {
	switch k {
	case ProposalCommit, ProposalPush, ProposalMergeRequest, ProposalInstall,
		ProposalPublish, ProposalAgentFile, ProposalDeleteFiles, ProposalSubtasks,
		ProposalMergeLocal, ProposalRemoveWorktree:
		return nil
	}
	return fmt.Errorf("unknown proposal kind %q", string(k))
}

// Validate reports whether s is a known proposal status.
func (s ProposalStatus) Validate() error {
	switch s {
	case ProposalPending, ProposalConfirmed, ProposalRejected, ProposalExecuted, ProposalFailed:
		return nil
	}
	return fmt.Errorf("unknown proposal status %q", string(s))
}

// Validate reports whether s is a known build status.
func (s BuildStatus) Validate() error {
	switch s {
	case BuildQueued, BuildRunning, BuildSucceeded, BuildFailed, BuildCancelled, BuildInterrupted:
		return nil
	}
	return fmt.Errorf("unknown build status %q", string(s))
}

// Validate reports whether s is a known step status.
func (s StepStatus) Validate() error {
	switch s {
	case StepQueued, StepRunning, StepSucceeded, StepFailed, StepCancelled, StepSkipped, StepInterrupted:
		return nil
	}
	return fmt.Errorf("unknown build step status %q", string(s))
}

// Validate reports whether o is a known step outcome.
func (o StepOutcome) Validate() error {
	switch o {
	case OutcomeSucceeded, OutcomeFailed, OutcomeStartFailed, OutcomeCancelled, OutcomeInterrupted:
		return nil
	}
	return fmt.Errorf("unknown build step outcome %q", string(o))
}

// Validate reports whether s is a known worktree status.
func (s WorktreeStatus) Validate() error {
	switch s {
	case WorktreeActive, WorktreeMerged, WorktreeRemoving, WorktreeRemoved:
		return nil
	}
	return fmt.Errorf("unknown worktree status %q", string(s))
}

// Validate reports whether s is a known change set status.
func (s ChangeSetStatus) Validate() error {
	switch s {
	case ChangeSetOpen, ChangeSetShipped, ChangeSetClosed:
		return nil
	}
	return fmt.Errorf("unknown change set status %q", string(s))
}
