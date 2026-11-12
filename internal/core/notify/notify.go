// Package notify is the functional core of notifications (UI-06, D11,
// docs/design/phase-6-notifications.md): which task and verification
// transitions become a notice, with per-source revisions so a replayed or older
// transition never notifies twice, a bounded recent history, and a separate
// durable high-water mark so trimming history never re-enables an old notice.
//
// A notice holds the task title and a status, plus metadata for navigation and
// deduplication. Nothing else can be put in one: no prompt, path, diff, log or
// arbitrary text (docs/AGENTS.md 5.9). The package does no IO and reads no
// clock; IDs and time arrive as input fields. It imports only internal/core.
package notify

import (
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Bounds.
const (
	// MaxRecent is the number of notices kept in history.
	MaxRecent = 200
	// MaxPage is the largest page of a history query.
	MaxPage = 200
	// MaxTitle bounds a title copied from the task.
	MaxTitle = 200
)

// Status is the displayable status of a notice, a closed set. Visible text is
// the task title and this status only.
type Status string

const (
	NeedsYou           Status = "needs_you"
	VerificationPassed Status = "verification_passed"
	VerificationFailed Status = "verification_failed"
	ReadyForReview     Status = "ready_for_review"
	Done               Status = "done"
	Failed             Status = "failed"
	Cancelled          Status = "cancelled"
)

// Statuses lists every status in a stable order.
func Statuses() []Status {
	return []Status{NeedsYou, VerificationPassed, VerificationFailed, ReadyForReview, Done, Failed, Cancelled}
}

// Validate reports whether s is a known status.
func (s Status) Validate() error {
	for _, x := range Statuses() {
		if s == x {
			return nil
		}
	}
	return fmt.Errorf("unknown notice status %q", string(s))
}

// OfTaskStatus maps a task status to the notice it produces, if any. Queued,
// running and draft are not notifiable; neither is a plain "answered" turn,
// which the task layer reports as review or done.
func OfTaskStatus(s core.TaskStatus) (Status, bool) {
	switch s {
	case core.TaskNeedsYou:
		return NeedsYou, true
	case core.TaskReview:
		return ReadyForReview, true
	case core.TaskDone:
		return Done, true
	case core.TaskFailed:
		return Failed, true
	case core.TaskCancelled:
		return Cancelled, true
	}
	return "", false
}

// SourceKind says what produced the transition.
type SourceKind string

const (
	SourceTask         SourceKind = "task"
	SourceVerification SourceKind = "verification"
)

// Source identifies the sequence a revision belongs to: the task's own status,
// or one verification run of the task (RunID set). Comparable, so it keys a map.
type Source struct {
	Kind  SourceKind
	RunID string
}

// Validate checks the shape: a verification source has a run; a task source
// has none.
func (s Source) Validate() error {
	switch s.Kind {
	case SourceTask:
		if s.RunID != "" {
			return errors.New("a task source has no run id")
		}
	case SourceVerification:
		if s.RunID == "" || len(s.RunID) > 100 {
			return errors.New("a verification source needs a run id")
		}
	default:
		return fmt.Errorf("unknown notice source %q", string(s.Kind))
	}
	return nil
}

// Key is the dedup identity of a producer sequence: (task, source).
type Key struct {
	Task   core.TaskID
	Source Source
}

// Notice is one notification. SourceRevision is the monotone revision of its
// source (the task row version, or the verification version) it was created
// from. Cursor is assigned by the store when the notice is committed (zero
// before) and orders history queries.
type Notice struct {
	ID             core.NoticeID
	Task           core.TaskID
	TaskTitle      string
	Status         Status
	Source         Source
	SourceRevision int64
	CreatedAt      time.Time
	Cursor         int64
}

// Validate checks that the notice can hold nothing but allowed fields.
func (n Notice) Validate() error {
	if n.ID.Validate() != nil || n.Task.Validate() != nil {
		return errors.New("a notice needs valid ids")
	}
	if n.TaskTitle == "" || len(n.TaskTitle) > MaxTitle {
		return errors.New("a notice needs a short task title")
	}
	if err := n.Status.Validate(); err != nil {
		return err
	}
	if err := n.Source.Validate(); err != nil {
		return err
	}
	if n.SourceRevision <= 0 {
		return errors.New("a notice needs a positive source revision")
	}
	return nil
}

// State is the producer state (sealed): Empty, Current or Closed.
//
//sumtype:decl
type State interface{ isState() }

// Empty: not recovered yet.
type Empty struct{}

// Current: running. HighWater is the highest revision seen per source and is
// durable and separate from Recent, which is bounded to MaxRecent.
type Current struct {
	HighWater map[Key]int64
	Recent    []Notice
}

// Closed: the producer stopped; nothing more is accepted.
type Closed struct{}

func (Empty) isState()   {}
func (Current) isState() {}
func (Closed) isState()  {}

// VerifyResult is the outcome of a verification transition. Queued and running
// are not results and never produce a notice.
type VerifyResult string

const (
	VerifyInProgress VerifyResult = "in_progress"
	VerifyPassed     VerifyResult = "passed"
	VerifyFailed     VerifyResult = "failed"
)

// Input is one event for Step (sealed).
//
//sumtype:decl
type Input interface{ isInput() }

// TaskTransition: a task committed a new status at Revision.
type TaskTransition struct {
	Task     core.TaskID
	Title    string
	Status   core.TaskStatus
	Revision int64
	Now      time.Time
	ID       core.NoticeID
}

// VerificationTransition: a verification of a task committed a new state at
// Revision for run RunID.
type VerificationTransition struct {
	Task     core.TaskID
	Title    string
	RunID    string
	Result   VerifyResult
	Revision int64
	Now      time.Time
	ID       core.NoticeID
}

// Recovered: sent once at startup with the stored history and the durable
// high-water marks.
type Recovered struct {
	Recent    []Notice
	HighWater map[Key]int64
}

// Stop: shut the producer down.
type Stop struct{}

func (TaskTransition) isInput()         {}
func (VerificationTransition) isInput() {}
func (Recovered) isInput()              {}
func (Stop) isInput()                   {}

// Command is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type Command interface{ isCommand() }

// PersistNoticeAndAudit stores, in one transaction, the notice, the new
// high-water mark, the retention trim and the audit entry.
type PersistNoticeAndAudit struct {
	Notice    Notice
	HighWater int64
}

// PersistHighWater stores only a new high-water mark (an uninteresting newer
// status).
type PersistHighWater struct {
	Key      Key
	Revision int64
}

// PublishNotice sends one invalidation with the notice's ID, after the commit.
type PublishNotice struct{ ID core.NoticeID }

func (PersistNoticeAndAudit) isCommand() {}
func (PersistHighWater) isCommand()      {}
func (PublishNotice) isCommand()         {}

// Step is (state2, commands) = Step(state1, input) following
// docs/design/phase-6-notifications.md.
//
// PENDING (task Q): the foundation freezes the types. Every input returns the
// state unchanged and no commands, so no notice is produced and none can carry
// a body.
func Step(s State, in Input) (State, []Command) {
	switch in.(type) {
	case TaskTransition, VerificationTransition, Recovered, Stop:
		return s, nil
	}
	return s, nil
}

// CursorGap reports whether a client that last saw cursor `after` missed
// notices because history was trimmed: some notice between its cursor and the
// oldest retained one is gone. after == 0 (a first load) and an empty history
// (oldest == 0) are never gaps.
func CursorGap(after, oldest int64) bool {
	return after > 0 && oldest > 0 && after+1 < oldest
}
