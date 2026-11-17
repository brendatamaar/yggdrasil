package proposalstest

// FakeNewRepoLifecycle is an in-memory stand-in for the durable new-repo
// lifecycle (internal/newrepos) behind the proposals port NewRepoLifecycle
// (ConfirmInit, ConfirmPublish, ReconcileOperation). It keeps a small state
// machine per operation, counts every call and every effect that took hold,
// injects one-shot faults (FailBefore: the call had no effect; FailAfter: the
// effect happened and the reply was lost) and reports state changes in the
// order they happen through OnChange, which is how the proposals tests mirror
// the lifecycle's state into the database and tell the service
// (Service.OperationChanged). It never touches a disk or a host.
//
// NewRepoLifecycleContract runs the port's rules against this fake; the real
// newrepos.Service runs the same suite when it is wired (the live run belongs
// to wiring, because it needs the reservation store and platform fakes).

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// OpPhase is the phase of a fake operation. It is a closed set.
type OpPhase string

const (
	PhaseIdle      OpPhase = "idle"
	PhaseRunning   OpPhase = "running"
	PhaseUncertain OpPhase = "uncertain"
	PhaseStopped   OpPhase = "stopped"
	PhaseReady     OpPhase = "ready"
	PhasePublished OpPhase = "published"
)

// OpFlow says which flow an operation is in.
type OpFlow string

const (
	FlowInit    OpFlow = "init"
	FlowPublish OpFlow = "publish"
)

// OpState is one operation of the fake.
type OpState struct {
	Phase   OpPhase
	Flow    OpFlow
	Hash    string // the immutable spec hash a confirm must match (init)
	PubHash string // the publish spec hash a publish confirm must match
	Attempt int    // the highest attempt the lifecycle has seen
	Reason  string // when stopped
}

// NewRepoCall is one call of the port.
type NewRepoCall struct {
	Method  string // "ConfirmInit", "ConfirmPublish" or "ReconcileOperation"
	Op      core.OperationID
	Hash    string
	Attempt int
}

// Change is one reported state change.
type Change struct {
	Op    core.OperationID
	State OpState
}

// ErrFakeRefused is returned when the fake refuses a confirm. It says that
// nothing was started.
type ErrFakeRefused struct{ Msg string }

func (e ErrFakeRefused) Error() string        { return e.Msg }
func (e ErrFakeRefused) UserMessage() string  { return e.Msg }
func (e ErrFakeRefused) NothingWritten() bool { return true }

// NewRepoFake is the fake lifecycle. Its zero value is not usable; call
// NewNewRepoFake.
type NewRepoFake struct {
	mu       sync.Mutex
	ops      map[core.OperationID]*OpState
	calls    []NewRepoCall
	effects  int
	fault    Fault
	faultErr error
	recTo    *OpPhase
	recStop  string
	onChange func(Change)
}

// NewNewRepoFake returns an empty fake.
func NewNewRepoFake() *NewRepoFake {
	return &NewRepoFake{ops: map[core.OperationID]*OpState{}}
}

// OnChange sets the function that receives every state change, in order. It is
// called without the fake's lock held, so it may call back into the service.
func (f *NewRepoFake) OnChange(fn func(Change)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

// AddInit registers an operation awaiting its initialization confirmation.
func (f *NewRepoFake) AddInit(op core.OperationID, specHash string) {
	f.mu.Lock()
	f.ops[op] = &OpState{Phase: PhaseIdle, Flow: FlowInit, Hash: specHash}
	f.mu.Unlock()
}

// AddReady registers an initialized operation awaiting a publish confirmation.
func (f *NewRepoFake) AddReady(op core.OperationID, publishHash string) {
	f.mu.Lock()
	f.ops[op] = &OpState{Phase: PhaseReady, Flow: FlowInit, PubHash: publishHash}
	f.mu.Unlock()
}

// SetPublishHash sets the publish spec hash an initialized operation accepts.
func (f *NewRepoFake) SetPublishHash(op core.OperationID, publishHash string) {
	f.mu.Lock()
	if s, ok := f.ops[op]; ok {
		s.PubHash = publishHash
	}
	f.mu.Unlock()
}

// Script makes the next ConfirmInit or ConfirmPublish fail as fault says with err.
func (f *NewRepoFake) Script(fault Fault, err error) {
	f.mu.Lock()
	f.fault, f.faultErr = fault, err
	f.mu.Unlock()
}

// ScriptReconcile makes the next ReconcileOperation move an uncertain or
// running operation to phase (PhaseReady, PhasePublished, PhaseStopped with
// reason, or PhaseIdle). Without it a reconcile changes nothing.
func (f *NewRepoFake) ScriptReconcile(to OpPhase, reason string) {
	f.mu.Lock()
	f.recTo, f.recStop = &to, reason
	f.mu.Unlock()
}

// State returns the operation's state.
func (f *NewRepoFake) State(op core.OperationID) (OpState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.ops[op]
	if !ok {
		return OpState{}, false
	}
	return *s, true
}

// Calls returns every call, in order; Effects counts the confirms that took
// hold (a started operation, including one whose reply was lost).
func (f *NewRepoFake) Calls() []NewRepoCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]NewRepoCall(nil), f.calls...)
}

// Confirms counts ConfirmInit and ConfirmPublish calls.
func (f *NewRepoFake) Confirms() int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method != "ReconcileOperation" {
			n++
		}
	}
	return n
}

// Reconciles counts ReconcileOperation calls.
func (f *NewRepoFake) Reconciles() int { return len(f.Calls()) - f.Confirms() }

// Effects counts the confirms that started or resumed work.
func (f *NewRepoFake) Effects() int { f.mu.Lock(); defer f.mu.Unlock(); return f.effects }

// move sets the operation's state and reports it (outside the lock).
func (f *NewRepoFake) move(op core.OperationID, mut func(*OpState)) {
	f.mu.Lock()
	s, ok := f.ops[op]
	if !ok {
		f.mu.Unlock()
		return
	}
	mut(s)
	st, fn := *s, f.onChange
	f.mu.Unlock()
	if fn != nil {
		fn(Change{Op: op, State: st})
	}
}

// Finish ends a running operation successfully: Ready after initialization,
// Published after publishing.
func (f *NewRepoFake) Finish(op core.OperationID) {
	f.move(op, func(s *OpState) {
		if s.Phase != PhaseRunning && s.Phase != PhaseUncertain {
			return
		}
		if s.Flow == FlowPublish {
			s.Phase = PhasePublished
		} else {
			s.Phase = PhaseReady
		}
	})
}

// Stop ends a running operation with a definite failure.
func (f *NewRepoFake) Stop(op core.OperationID, reason string) {
	f.move(op, func(s *OpState) {
		if s.Phase == PhaseRunning || s.Phase == PhaseUncertain {
			s.Phase, s.Reason = PhaseStopped, reason
		}
	})
}

// GoUncertain makes a running operation uncertain (a restart, a lost reply).
func (f *NewRepoFake) GoUncertain(op core.OperationID) {
	f.move(op, func(s *OpState) {
		if s.Phase == PhaseRunning {
			s.Phase = PhaseUncertain
		}
	})
}

func (f *NewRepoFake) record(c NewRepoCall) (fault Fault, ferr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if c.Method == "ReconcileOperation" {
		return NoFault, nil
	}
	fault, ferr = f.fault, f.faultErr
	f.fault, f.faultErr = NoFault, nil
	if fault != NoFault && ferr == nil {
		ferr = errors.New("the new-repo service did not answer")
	}
	return fault, ferr
}

// confirm is the shared rule of both confirms: a known operation, the immutable
// hash, a fresh attempt. An operation that is already running, uncertain or
// done takes no second start (the duplicate returns nil with no effect); a
// stopped one resumes only with a strictly greater attempt.
func (f *NewRepoFake) confirm(ctx context.Context, method string, op core.OperationID, hash string, attempt int, flow OpFlow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fault, ferr := f.record(NewRepoCall{Method: method, Op: op, Hash: hash, Attempt: attempt})
	if fault == FailBefore {
		return ferr
	}
	var start bool
	var refusal error
	f.mu.Lock()
	s, ok := f.ops[op]
	switch {
	case !ok:
		refusal = ErrFakeRefused{Msg: "There is no such new repo operation."}
	case attempt < 1:
		refusal = ErrFakeRefused{Msg: "The attempt number is not valid."}
	case flow == FlowInit && hash != s.Hash, flow == FlowPublish && hash != s.PubHash:
		refusal = ErrFakeRefused{Msg: "What was confirmed is not what this operation holds."}
	case flow == FlowInit && s.Phase == PhaseIdle, flow == FlowPublish && s.Phase == PhaseReady:
		start = attempt > s.Attempt
		if !start {
			refusal = ErrFakeRefused{Msg: "That attempt was already used."}
		}
	case s.Phase == PhaseStopped && s.Flow == flow:
		start = attempt > s.Attempt
		if !start {
			refusal = ErrFakeRefused{Msg: "A retry needs a newer attempt than the one that stopped."}
		}
	case flow == FlowPublish && s.Phase == PhaseIdle:
		refusal = ErrFakeRefused{Msg: "The repo is not ready to publish."}
	}
	f.mu.Unlock()
	if refusal != nil {
		return refusal
	}
	if start {
		f.mu.Lock()
		f.effects++
		f.mu.Unlock()
		f.move(op, func(s *OpState) {
			s.Phase, s.Flow, s.Attempt, s.Reason = PhaseRunning, flow, attempt, ""
		})
	}
	if fault == FailAfter {
		return ferr
	}
	return nil
}

// ConfirmInit implements the port.
func (f *NewRepoFake) ConfirmInit(ctx context.Context, op core.OperationID, hash string, attempt int) error {
	return f.confirm(ctx, "ConfirmInit", op, hash, attempt, FlowInit)
}

// ConfirmPublish implements the port.
func (f *NewRepoFake) ConfirmPublish(ctx context.Context, op core.OperationID, hash string, attempt int) error {
	return f.confirm(ctx, "ConfirmPublish", op, hash, attempt, FlowPublish)
}

// ReconcileOperation implements the port: read-only unless a result was
// scripted, in which case it settles an uncertain or running operation.
func (f *NewRepoFake) ReconcileOperation(ctx context.Context, op core.OperationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _ = f.record(NewRepoCall{Method: "ReconcileOperation", Op: op})
	f.mu.Lock()
	s, ok := f.ops[op]
	to, reason := f.recTo, f.recStop
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("reconcile %s: %w", op, ErrFakeRefused{Msg: "There is no such new repo operation."})
	}
	settle := to != nil && (s.Phase == PhaseUncertain || s.Phase == PhaseRunning)
	if settle {
		f.recTo = nil
	}
	f.mu.Unlock()
	if !settle {
		return nil
	}
	switch *to {
	case PhaseReady, PhasePublished:
		f.Finish(op)
	case PhaseStopped:
		f.Stop(op, reason)
	case PhaseIdle:
		f.move(op, func(s *OpState) { s.Phase = PhaseIdle })
	}
	return nil
}
