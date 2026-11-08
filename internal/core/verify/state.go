package verify

import (
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// VerificationID identifies a verification row.
type VerificationID string

// Validate reports whether the ID is well formed.
func (id VerificationID) Validate() error {
	return core.TaskID(id).Validate()
}

// Verification is the aggregate for one (task, repo). BootHash is the
// dependency hash the last successful bootstrap saw ("" = never).
type Verification struct {
	ID       VerificationID
	Task     core.TaskID
	Repo     core.RepoID
	Worktree core.WorktreeID
	BootHash string
	State    State
}

// Validate checks the identity and that the state is set and valid.
func (v Verification) Validate() error {
	if err := v.ID.Validate(); err != nil {
		return err
	}
	if err := v.Task.Validate(); err != nil {
		return err
	}
	if err := v.Repo.Validate(); err != nil {
		return err
	}
	if err := v.Worktree.Validate(); err != nil {
		return err
	}
	if v.State == nil {
		return errors.New("verification has no state")
	}
	return v.State.Validate()
}

// State is the state of one verification (sealed). "Superseded", "queued
// behind another build" and "retrying" are not states (see verify.md).
type State interface {
	Validate() error
	isState()
}

// None: no turn has ended in review yet.
type None struct{}

// Skipped: nothing to verify, by rule.
type Skipped struct {
	Turn    TurnID
	TurnSeq int
	Reason  SkipReason
}

// Unverifiable: wanted to verify, could not.
type Unverifiable struct {
	Turn    TurnID
	TurnSeq int
	// Attempt continues the count of the turn (see Retry).
	Attempt Attempt
	Reason  UnverifiableReason
}

// Bootstrapping: the bootstrap build runs; Plan is kept so the verify build
// starts without a new read.
type Bootstrapping struct {
	Turn      TurnID
	TurnSeq   int
	Attempt   Attempt
	Build     core.BuildID
	Plan      Plan
	NextBuild core.BuildID
	DepsHash  string
	Since     time.Time
}

// Verifying: the verify build runs.
type Verifying struct {
	Turn    TurnID
	TurnSeq int
	Attempt Attempt
	Build   core.BuildID
	Plan    Plan
	Since   time.Time
}

// Passed: the verify chain succeeded.
type Passed struct {
	Turn    TurnID
	TurnSeq int
	Attempt Attempt
	Build   core.BuildID
	At      time.Time
}

// Failed: a step failed.
type Failed struct {
	Turn    TurnID
	TurnSeq int
	Attempt Attempt
	Stage   Stage
	Build   core.BuildID
	// Step is the index in the stage's chain.
	Step   int
	Reason FailReason
	At     time.Time
}

// Interrupted: the process was lost (ygg stopped).
type Interrupted struct {
	Turn    TurnID
	TurnSeq int
	Attempt Attempt
	Stage   Stage
	At      time.Time
}

// Cancelled: the build was cancelled.
type Cancelled struct {
	Turn    TurnID
	TurnSeq int
	Attempt Attempt
	Cause   CancelCause
	At      time.Time
}

func (None) isState()          {}
func (Skipped) isState()       {}
func (Unverifiable) isState()  {}
func (Bootstrapping) isState() {}
func (Verifying) isState()     {}
func (Passed) isState()        {}
func (Failed) isState()        {}
func (Interrupted) isState()   {}
func (Cancelled) isState()     {}

// Validate always succeeds: None carries no data.
func (None) Validate() error { return nil }

// Validate checks the turn and reason.
func (s Skipped) Validate() error {
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Reason.Validate())
}

// Validate checks the turn and reason.
func (s Unverifiable) Validate() error {
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Reason.Validate())
}

// Validate checks the turn, attempt, builds and that the plan has a bootstrap
// chain (V9).
func (s Bootstrapping) Validate() error {
	if len(s.Plan.Bootstrap) == 0 {
		return errors.New("a bootstrapping verification needs bootstrap steps")
	}
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Build.Validate(), s.NextBuild.Validate(), s.Plan.Validate())
}

// Validate checks the turn, attempt, build and that the plan has a verify
// chain (V9).
func (s Verifying) Validate() error {
	if len(s.Plan.Verify) == 0 {
		return errors.New("a verifying verification needs verify steps")
	}
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Build.Validate(), s.Plan.Validate())
}

// Validate checks the turn, attempt and build.
func (s Passed) Validate() error {
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Build.Validate())
}

// Validate checks the identity fields, the step index and the reason.
func (s Failed) Validate() error {
	if s.Step < 0 {
		return fmt.Errorf("failed step index %d is negative", s.Step)
	}
	if s.Reason == nil {
		return errors.New("a failed verification needs a reason")
	}
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Stage.Validate(), s.Build.Validate(), s.Reason.Validate())
}

// Validate checks the turn, attempt and stage.
func (s Interrupted) Validate() error {
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Stage.Validate())
}

// Validate checks the turn and cause.
func (s Cancelled) Validate() error {
	return errors.Join(s.Turn.Validate(), validSeq(s.TurnSeq), s.Attempt.Validate(), s.Cause.Validate())
}

// validSeq reports whether n is a valid turn sequence number (1 or more).
func validSeq(n int) error {
	if n < 1 {
		return fmt.Errorf("turn sequence %d: must be 1 or more", n)
	}
	return nil
}
