package worktreeenv

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// RelPath is a validated repo-relative path with forward slashes. Only the
// ParseProfile / ParseComposeFile parsers make one.
type RelPath string

// Range is the inclusive port range blocks are taken from.
type Range struct{ Lo, Hi int }

// DefaultRange is 20000-29999 (decision 2).
var DefaultRange = Range{Lo: 20000, Hi: 29999}

// Profile copy limits, in one place.
const (
	MaxEntries   = 50
	MaxFileBytes = 1 << 20
	MaxTotalByte = 5 << 20
)

// Trigger says who asked for a profile copy.
type Trigger string

// The closed set of Trigger values.
const (
	TriggerUserStarted   Trigger = "user_started"
	TriggerUserConfirmed Trigger = "user_confirmed"
	TriggerAgentTask     Trigger = "agent_task"
)

// SkipReason says why an entry was not copied.
type SkipReason string

// The closed set of SkipReason values.
const (
	SkipSourceMissing   SkipReason = "source_missing"
	SkipNotRegularFile  SkipReason = "not_regular_file"
	SkipDestExists      SkipReason = "dest_exists"
	SkipTooLarge        SkipReason = "too_large"
	SkipBudgetExceeded  SkipReason = "budget_exceeded"
	SkipAgentTasksOff   SkipReason = "agent_tasks_off"
	SkipSourceUnreadble SkipReason = "source_unreadable"
)

// Source is what Lstat found for a profile entry in the repo.
type Source interface{ isSource() }

// SourceFile is a regular file of Size bytes.
type SourceFile struct{ Size int64 }

// SourceMissing means the entry does not exist.
type SourceMissing struct{}

// SourceDirectory means the entry is a directory.
type SourceDirectory struct{}

// SourceLink means the entry, or one of its parents, is a symlink or reparse point.
type SourceLink struct{}

// SourceUnreadable means the entry could not be inspected.
type SourceUnreadable struct{}

func (SourceFile) isSource()       {}
func (SourceMissing) isSource()    {}
func (SourceDirectory) isSource()  {}
func (SourceLink) isSource()       {}
func (SourceUnreadable) isSource() {}

// Dest is what is at the destination in the worktree.
type Dest string

// The closed set of Dest values.
const (
	DestAbsent Dest = "absent"
	DestExists Dest = "exists"
)

// ComposeOp is the closed set of compose operations. The command has no
// field for extra arguments, so -v, --volumes, --rmi and --remove-orphans
// cannot be expressed (WE-03, decision 1).
type ComposeOp string

// The closed set of ComposeOp values.
const (
	OpUp   ComposeOp = "up"
	OpStop ComposeOp = "stop"
	OpDown ComposeOp = "down"
	OpPs   ComposeOp = "ps"
)

// Block is the port block of one worktree.
type Block = core.PortBlock

// Now is a convenience alias to keep input structs short.
type Now = time.Time
