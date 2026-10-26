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

// TurnAborted: the turn was cancelled or crashed.
type TurnAborted struct{ Turn TurnID }

func (TurnAborted) isIntegrityInput()    {}
func (TurnStarts) isIntegrityInput()     {}
func (SnapshotTaken) isIntegrityInput()  {}
func (SnapshotFailed) isIntegrityInput() {}
func (TurnEnds) isIntegrityInput()       {}

// StepIntegrity is (state, input) -> (state, commands) for the integrity
// check (the StepIntegrity table of docs/design/guard.md). Any pair it does
// not handle, including an input for another turn, changes nothing.
//
// A snapshot with an empty field proves nothing, so it is Unverifiable, never
// Clean. A TurnAborted (cancelled or crashed turn) before the run started
// goes back to Idle; after it started it is Unverifiable, because the tree
// was not checked. No StopTask is emitted for an abort: the turn is already
// stopped. A TurnStarts for a new turn ID starts fresh from any terminal
// state (Ask: Arming; other modes: Idle).
func StepIntegrity(s Integrity, in IntegrityInput) (Integrity, []Command) {
	// S2: a different turn replaces a live check. The old turn gets its
	// Unverifiable verdict as commands, then the new turn starts fresh.
	if ts, ok := in.(TurnStarts); ok {
		if old := liveTurn(s); old != "" && ts.Turn != old {
			next, cmds := startTurn(IntegrityIdle{}, in)
			return next, append(supersede(old), cmds...)
		}
	}
	switch st := s.(type) {
	case IntegrityIdle:
		return startTurn(s, in)
	case Clean:
		if ts, ok := in.(TurnStarts); ok && ts.Turn != st.Turn {
			return startTurn(IntegrityIdle{}, in)
		}
	case Violated:
		if ts, ok := in.(TurnStarts); ok && ts.Turn != st.Turn {
			return startTurn(IntegrityIdle{}, in)
		}
	case Unverifiable:
		if ts, ok := in.(TurnStarts); ok && ts.Turn != st.Turn {
			return startTurn(IntegrityIdle{}, in)
		}
	case Arming:
		switch in := in.(type) {
		case SnapshotTaken:
			if in.Turn == st.Turn && in.Phase == PhaseBefore {
				if !in.Snap.complete() {
					return unverifiable(st.Turn, "the snapshot before the turn was incomplete")
				}
				return Armed{Turn: st.Turn, Before: in.Snap}, nil
			}
		case SnapshotFailed:
			if in.Turn == st.Turn && in.Phase == PhaseBefore {
				return unverifiable(st.Turn, in.Reason)
			}
		case TurnAborted:
			if in.Turn == st.Turn {
				return IntegrityIdle{}, nil
			}
		case TurnEnds:
			// The run ended before the Before snapshot returned.
			if in.Turn == st.Turn {
				return unverifiable(st.Turn, "the turn ended before the snapshot before it was taken")
			}
		}
	case Armed:
		switch in := in.(type) {
		case TurnEnds:
			if in.Turn == st.Turn {
				return Checking(st), []Command{TakeSnapshot{Turn: st.Turn, Phase: PhaseAfter}}
			}
		case TurnAborted:
			if in.Turn == st.Turn {
				return aborted(st.Turn)
			}
		}
	case Checking:
		switch in := in.(type) {
		case TurnAborted:
			if in.Turn == st.Turn {
				return aborted(st.Turn)
			}
		case SnapshotTaken:
			if in.Turn != st.Turn || in.Phase != PhaseAfter {
				break
			}
			if !in.Snap.complete() || !st.Before.complete() {
				return unverifiable(st.Turn, "the snapshot after the turn was incomplete")
			}
			if st.Before.Equal(in.Snap) {
				return Clean{Turn: st.Turn}, []Command{
					Audit{Entry: AuditEntry{Action: "guard.integrity", Outcome: "clean", Turn: st.Turn}},
				}
			}
			changes := diff(in.Snap)
			return Violated{Turn: st.Turn, Changes: changes}, []Command{
				StopTask{Turn: st.Turn, Reason: StopTreeChanged},
				FlagTask{Turn: st.Turn, Changes: changes},
				Audit{Entry: AuditEntry{Action: "guard.integrity", Outcome: "violated", Turn: st.Turn, Files: changePaths(changes)}},
			}
		case SnapshotFailed:
			if in.Turn == st.Turn && in.Phase == PhaseAfter {
				return unverifiable(st.Turn, in.Reason)
			}
		}
	}
	return s, nil
}

// startTurn handles TurnStarts from a state with no live check.
func startTurn(s Integrity, in IntegrityInput) (Integrity, []Command) {
	ts, ok := in.(TurnStarts)
	if !ok {
		return s, nil
	}
	if ts.Mode == core.ModeAsk {
		return Arming{Turn: ts.Turn}, []Command{TakeSnapshot{Turn: ts.Turn, Phase: PhaseBefore}}
	}
	return IntegrityIdle{}, nil
}

// aborted: the turn ended without its After check. The task is already
// stopped, so there is no StopTask; the user sees the Unverifiable state.
func aborted(t TurnID) (Integrity, []Command) {
	return Unverifiable{Turn: t, Why: "the turn was cancelled or crashed before it could be checked"}, supersede(t)
}

// liveTurn is the turn of a check in flight, or "".
func liveTurn(s Integrity) TurnID {
	switch st := s.(type) {
	case Arming:
		return st.Turn
	case Armed:
		return st.Turn
	case Checking:
		return st.Turn
	}
	return ""
}

// supersede tells the shell a turn could not be verified without stopping it
// (it is already stopped or replaced): an audit entry and a flag for the user.
func supersede(t TurnID) []Command {
	return []Command{
		Audit{Entry: AuditEntry{Action: "guard.integrity", Outcome: "unverifiable", Turn: t}},
		FlagTask{Turn: t},
	}
}

// complete reports whether the snapshot has all three fingerprints.
func (s Snapshot) complete() bool { return s.Head != "" && s.Status != "" && s.DiffHash != "" }

func unverifiable(t TurnID, why string) (Integrity, []Command) {
	return Unverifiable{Turn: t, Why: why}, []Command{
		StopTask{Turn: t, Reason: StopCannotVerify},
		Audit{Entry: AuditEntry{Action: "guard.integrity", Outcome: "unverifiable", Turn: t}},
	}
}

// diff lists the paths the shell attached to the After snapshot.
func diff(after Snapshot) []Change {
	var out []Change
	for _, p := range after.Paths {
		out = append(out, Change{Path: p})
	}
	return out
}

func changePaths(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}
