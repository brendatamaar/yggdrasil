// Package standup is the functional core of the standup summary (AU-02,
// UI-03, docs/design/phase-6-standup.md): a deterministic, bounded aggregate of
// commits, merge requests and builds for one project and one date range, with
// partial and unavailable sources labelled and never invented. It does no IO
// and reads no clock. It imports only internal/core.
//
// A summary refresh never starts an agent. Polishing the text is an explicit
// user submission through the ordinary action/task path.
package standup

import (
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Bounds (design "Shapes and state map").
const (
	MaxRangeDays        = 31
	MaxRepos            = 50
	MaxRecordsPerSource = 200
	MaxTextBytes        = 64 << 10
	// ReadTimeout bounds one whole aggregate read.
	ReadTimeout = 20 * time.Second
)

// Range is a project and a half-open UTC interval [From, To), at most
// MaxRangeDays long. The browser supplies local-day boundaries; the server
// validates and converts them.
type Range struct {
	Project core.ProjectID
	From    time.Time
	To      time.Time
}

// ErrInvalidRange wraps every rejection by ParseRange.
var ErrInvalidRange = errors.New("invalid standup range")

// ParseRange validates a range: a valid project, From before To, at most
// MaxRangeDays apart, both converted to UTC.
func ParseRange(project core.ProjectID, from, to time.Time) (Range, error) {
	if err := project.Validate(); err != nil {
		return Range{}, fmt.Errorf("%w: the project id is not valid", ErrInvalidRange)
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return Range{}, fmt.Errorf("%w: the start must be before the end", ErrInvalidRange)
	}
	if to.Sub(from) > MaxRangeDays*24*time.Hour {
		return Range{}, fmt.Errorf("%w: the range is longer than %d days", ErrInvalidRange, MaxRangeDays)
	}
	return Range{Project: project, From: from.UTC(), To: to.UTC()}, nil
}

// SourceKind names a data source. It is a closed set.
type SourceKind string

const (
	SourceCommits SourceKind = "commits"
	SourceMRs     SourceKind = "merge_requests"
	SourceBuilds  SourceKind = "builds"
)

// Validate reports whether k is known.
func (k SourceKind) Validate() error {
	switch k {
	case SourceCommits, SourceMRs, SourceBuilds:
		return nil
	}
	return fmt.Errorf("unknown standup source %q", string(k))
}

// Commit is one commit of a tracked repo in the range.
type Commit struct {
	Repo     core.RepoID
	RepoName string
	OID      string
	Subject  string
	Author   string
	At       time.Time
}

// MR is one merge request from the cached host data. Number and State come
// from the host; Title is plain text.
type MR struct {
	Repo     core.RepoID
	RepoName string
	Number   int
	Title    string
	State    string
	At       time.Time
}

// Build is one build of a tracked repo in the range.
type Build struct {
	Repo     core.RepoID
	RepoName string
	ID       core.BuildID
	Status   core.BuildStatus
	At       time.Time
}

// Unavailable labels a source or repo that could not be read, so the summary
// says so instead of leaving a silent gap.
type Unavailable struct {
	Source SourceKind
	// Repo is empty when the whole source is unavailable.
	Repo core.RepoID
	// RepoName is optional: the text names the repo with it when present.
	RepoName string
	Reason   string
}

// Summary is the typed content of a standup. Truncated names the sources cut at
// MaxRecordsPerSource or the text bound.
type Summary struct {
	Range     Range
	Commits   []Commit
	MRs       []MR
	Builds    []Build
	Truncated []SourceKind
	// Text is the deterministic rendering, at most MaxTextBytes.
	Text string
}

// State is the state of one standup read (sealed): Empty, Loading, Ready,
// Partial or Failed.
//
//sumtype:decl
type State interface{ isState() }

// Empty: nothing requested, or cancelled.
type Empty struct{}

// Loading: attempt Attempt reads Range.
type Loading struct {
	Attempt int
	Range   Range
}

// Ready: every source was readable (the summary may be empty: that is a
// result, not a failure).
type Ready struct{ Summary Summary }

// Partial: some sources were unavailable, labelled in Unavailable.
type Partial struct {
	Summary     Summary
	Unavailable []Unavailable
}

// Failed: no source was readable.
type Failed struct{ Reason string }

func (Empty) isState()   {}
func (Loading) isState() {}
func (Ready) isState()   {}
func (Partial) isState() {}
func (Failed) isState()  {}

// Standup is the header (the highest attempt issued) plus the sealed State.
type Standup struct {
	Seq   int
	State State
}

// Input is one event for Step (sealed).
//
//sumtype:decl
type Input interface{ isInput() }

// Requested: a valid range was requested.
type Requested struct {
	Range Range
	Now   time.Time
}

// SourcesRead: Attempt read the sources; Unavailable labels what was missing.
type SourcesRead struct {
	Attempt     int
	Commits     []Commit
	MRs         []MR
	Builds      []Build
	Unavailable []Unavailable
	// Readable is the number of sources that were readable (0 to 3). It must
	// equal 3 minus the sources labelled wholly unavailable.
	Readable int
	// Truncated names the sources the shell cut at the per-repo cap (the
	// reader said there was more). Additive field.
	Truncated []SourceKind
}

// ReadFailed: Attempt failed before reading any source.
type ReadFailed struct {
	Attempt int
	Reason  string
}

// Cancelled: the caller gave up on an attempt.
type Cancelled struct{ Attempt int }

// PolishRequested: the user asked to polish the displayed text. RequestID keys
// the ordinary action/task submission.
type PolishRequested struct{ RequestID core.RequestID }

func (Requested) isInput()       {}
func (SourcesRead) isInput()     {}
func (ReadFailed) isInput()      {}
func (Cancelled) isInput()       {}
func (PolishRequested) isInput() {}

// Command is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type Command interface{ isCommand() }

// ReadSources: one bounded aggregate read (20 s), cancelling any earlier one.
type ReadSources struct {
	Attempt int
	Range   Range
}

// ReturnSummary answers the caller.
type ReturnSummary struct{ Attempt int }

// Explain tells the caller why there is no summary.
type Explain struct{ Reason string }

// CancelRead stops the read of an attempt.
type CancelRead struct{ Attempt int }

// SubmitAskDraft submits the polish request through the ordinary action path
// (Ask mode, RequestID-bound). It is only emitted for explicit user input.
type SubmitAskDraft struct {
	RequestID core.RequestID
	Text      string
}

func (ReadSources) isCommand()    {}
func (ReturnSummary) isCommand()  {}
func (Explain) isCommand()        {}
func (CancelRead) isCommand()     {}
func (SubmitAskDraft) isCommand() {}
