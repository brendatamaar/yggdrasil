package guard

import "github.com/brendatamaar/yggdrasil/internal/core"

// Integrity is the state of the Ask-mode integrity check of one turn (SF-07).
// It is sealed: IntegrityIdle, Arming, Armed, Checking, Clean, Violated or
// Unverifiable. Clean, Violated and Unverifiable are terminal.
type Integrity interface{ isIntegrity() }

// IntegrityIdle: no check in flight.
type IntegrityIdle struct{}

// Arming: the Before snapshot was requested.
type Arming struct{ Turn TurnID }

// Armed: the Before snapshot is taken; the run may start.
type Armed struct {
	Turn   TurnID
	Before Snapshot
}

// Checking: the turn ended and the After snapshot was requested.
type Checking struct {
	Turn   TurnID
	Before Snapshot
}

// Clean: the tree is unchanged (terminal).
type Clean struct{ Turn TurnID }

// Violated: the tree changed during a read-only turn (terminal).
type Violated struct {
	Turn    TurnID
	Changes []Change
}

// Unverifiable: a snapshot failed, so the check cannot say; counts as a
// violation (terminal).
type Unverifiable struct {
	Turn TurnID
	Why  string
}

func (IntegrityIdle) isIntegrity() {}
func (Arming) isIntegrity()        {}
func (Armed) isIntegrity()         {}
func (Checking) isIntegrity()      {}
func (Clean) isIntegrity()         {}
func (Violated) isIntegrity()      {}
func (Unverifiable) isIntegrity()  {}

// Phase says which snapshot of a turn is meant.
type Phase string

const (
	PhaseBefore Phase = "before"
	PhaseAfter  Phase = "after"
)

// IntegrityInput is an input of StepIntegrity (sealed): TurnStarts,
// SnapshotTaken, SnapshotFailed or TurnEnds.
type IntegrityInput interface{ isIntegrityInput() }

// TurnStarts: a turn is about to run in Mode.
type TurnStarts struct {
	Turn TurnID
	Mode core.TaskMode
}

// SnapshotTaken: the shell took the snapshot a TakeSnapshot command asked for.
type SnapshotTaken struct {
	Turn  TurnID
	Phase Phase
	Snap  Snapshot
}

// SnapshotFailed: the shell could not take it, after its own retry.
type SnapshotFailed struct {
	Turn   TurnID
	Phase  Phase
	Reason string
}

// TurnEnds: the run of the turn finished.
type TurnEnds struct{ Turn TurnID }

func (TurnStarts) isIntegrityInput()     {}
func (SnapshotTaken) isIntegrityInput()  {}
func (SnapshotFailed) isIntegrityInput() {}
func (TurnEnds) isIntegrityInput()       {}

// StepIntegrity is (state, input) -> (state, commands) for the integrity
// check (the StepIntegrity table of docs/design/guard.md). Any pair it does
// not handle, including an input for another turn, changes nothing.
//
// STUB (task G1 implements it): returns the state unchanged and no commands.
func StepIntegrity(s Integrity, in IntegrityInput) (Integrity, []Command) {
	_ = in
	return s, nil
}
