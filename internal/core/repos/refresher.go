package repos

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Defaults of the status refresher (docs/design/repo-status.md).
const (
	// DefaultMax is the number of concurrent reads.
	DefaultMax = 8
	// TickEvery is the interval of the shell's Tick ticker.
	TickEvery = 30 * time.Second
	// MinAgeFocus and MinAgeTick: focus and tick requests skip repos whose
	// view is younger than this. Other reasons always enqueue.
	MinAgeFocus = 15 * time.Second
	MinAgeTick  = 2 * time.Minute
	// ReadTimeout is the shell's limit for one read.
	ReadTimeout = 15 * time.Second
)

// Refresher is the state of the status scheduler: the read model plus the
// queue of repos waiting to be read, with bounded concurrency.
type Refresher struct {
	Entries map[core.RepoID]Entry
	// Queue holds each ID at most once.
	Queue []core.RepoID
	// InFlight maps a repo to the attempt being read.
	InFlight map[core.RepoID]int
	// Max is the number of concurrent reads.
	Max int
}

// Entry is one repo's status.
type Entry struct {
	Path CleanPath
	// Attempt is the last attempt started.
	Attempt int
	View    View
}

// RefreshReason says why a refresh was requested.
type RefreshReason string

const (
	ReasonUser      RefreshReason = "user"
	ReasonFocus     RefreshReason = "focus"
	ReasonGitAction RefreshReason = "git_action"
	ReasonBuild     RefreshReason = "build"
	ReasonTracking  RefreshReason = "tracking"
	ReasonTick      RefreshReason = "tick"
)

// Validate reports whether r is a known reason.
func (r RefreshReason) Validate() error {
	switch r {
	case ReasonUser, ReasonFocus, ReasonGitAction, ReasonBuild, ReasonTracking, ReasonTick:
		return nil
	}
	return fmt.Errorf("unknown refresh reason %q", string(r))
}

// RefreshInput is one event for Refresh. It is a closed set.
type RefreshInput interface{ isRefreshInput() }

// RepoRef is an active repo and where it lives.
type RepoRef struct {
	ID   core.RepoID
	Path CleanPath
}

// ReposChanged carries the full active set (from tracking, and at startup).
type ReposChanged struct{ Active []RepoRef }

// RefreshRequested asks to read repos; empty IDs means all.
type RefreshRequested struct {
	IDs    []core.RepoID
	Reason RefreshReason
	Now    time.Time
}

// Tick is the shell ticker.
type Tick struct{ Now time.Time }

// StatusRead is a successful read.
type StatusRead struct {
	ID      core.RepoID
	Attempt int
	Snap    Snapshot
	Now     time.Time
}

// StatusFailed is a failed read.
type StatusFailed struct {
	ID      core.RepoID
	Attempt int
	Kind    ProblemKind
	Now     time.Time
}

func (ReposChanged) isRefreshInput()     {}
func (RefreshRequested) isRefreshInput() {}
func (Tick) isRefreshInput()             {}
func (StatusRead) isRefreshInput()       {}
func (StatusFailed) isRefreshInput()     {}

// RefreshCommand is an effect Refresh asks for. It is a closed set.
type RefreshCommand interface{ isRefreshCommand() }

// ReadStatus reads one repo: read-only git, deduplicated by (ID, Attempt).
type ReadStatus struct {
	ID      core.RepoID
	Path    CleanPath
	Attempt int
}

// PublishStatus tells subscribers that one repo's view changed.
type PublishStatus struct{ ID core.RepoID }

// PublishCounts tells subscribers that the counts changed.
type PublishCounts struct{}

func (ReadStatus) isRefreshCommand()    {}
func (PublishStatus) isRefreshCommand() {}
func (PublishCounts) isRefreshCommand() {}

// Refresh is the status scheduler step: (state2, commands) =
// Refresh(state1, input). An input that doesn't apply (an unknown ID, a stale
// attempt) returns the state unchanged and no commands.
func Refresh(r Refresher, in RefreshInput) (Refresher, []RefreshCommand) {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return r, nil
}

// RunRefresh feeds inputs to Refresh in order and returns the final state and
// every command in order. The property tests drive Refresh through it.
func RunRefresh(r Refresher, inputs []RefreshInput) (Refresher, []RefreshCommand) {
	var all []RefreshCommand
	for _, in := range inputs {
		var cmds []RefreshCommand
		r, cmds = Refresh(r, in)
		all = append(all, cmds...)
	}
	return r, all
}
