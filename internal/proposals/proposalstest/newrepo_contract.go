package proposalstest

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Lifecycle is the method set of the proposals port NewRepoLifecycle (declared
// again here because this package cannot import internal/proposals).
type Lifecycle interface {
	ConfirmInit(ctx context.Context, op core.OperationID, hash string, attempt int) error
	ConfirmPublish(ctx context.Context, op core.OperationID, hash string, attempt int) error
	ReconcileOperation(ctx context.Context, op core.OperationID) error
}

// NewRepoRig is a lifecycle under test with an operation awaiting its
// initialization confirmation (Init, whose immutable spec hash is InitHash) and
// an initialized operation awaiting a publish confirmation (Ready, PublishHash).
// Phase reads an operation's phase and Effects counts the confirms that took
// hold (work started or resumed). Finish lets the durable worker end a running
// operation successfully.
type NewRepoRig struct {
	L           Lifecycle
	Init        core.OperationID
	InitHash    string
	Ready       core.OperationID
	PublishHash string
	Phase       func(op core.OperationID) OpPhase
	Effects     func() int
	Finish      func(op core.OperationID)
}

// NewRepoLifecycleContract is what the InitRepo and PublishRepo executors rely
// on from the lifecycle: a confirm with the immutable spec hash starts work and
// returns once the worker owns it; a wrong hash, an unknown operation or an
// operation not ready is refused and starts nothing; the same attempt twice
// starts nothing twice; a stale attempt starts nothing; a read-only reconcile
// never starts work. The fake runs it (newrepo_contract_test.go); the real
// newrepos.Service must run the same suite when it is wired.
func NewRepoLifecycleContract(t *testing.T, mk func(t *testing.T) NewRepoRig) {
	t.Helper()
	ctx := context.Background()
	t.Run("a matching hash starts the work and the worker owns it", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, r.Init, r.InitHash, 1); err != nil {
			t.Fatalf("ConfirmInit = %v", err)
		}
		if p := r.Phase(r.Init); p != PhaseRunning && p != PhaseReady {
			t.Fatalf("phase = %s, want running or ready", p)
		}
		if r.Effects() != 1 {
			t.Fatalf("effects = %d, want 1", r.Effects())
		}
	})
	t.Run("a different hash is refused and nothing starts", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, r.Init, "0000000000000000000000000000000000000000000000000000000000000000", 1); err == nil {
			t.Fatal("ConfirmInit accepted a different hash")
		}
		if r.Phase(r.Init) != PhaseIdle || r.Effects() != 0 {
			t.Fatalf("phase %s, effects %d after a refused confirm", r.Phase(r.Init), r.Effects())
		}
	})
	t.Run("an unknown operation is refused", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, "op_unknown", r.InitHash, 1); err == nil {
			t.Fatal("ConfirmInit accepted an unknown operation")
		}
		if r.Effects() != 0 {
			t.Fatal("work started for an unknown operation")
		}
	})
	t.Run("the same attempt twice starts nothing twice", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, r.Init, r.InitHash, 1); err != nil {
			t.Fatal(err)
		}
		_ = r.L.ConfirmInit(ctx, r.Init, r.InitHash, 1) // may be a no-op or a refusal, never a second start
		if r.Effects() != 1 {
			t.Fatalf("effects = %d after a duplicate confirm, want 1", r.Effects())
		}
	})
	t.Run("a confirm on a running operation starts nothing", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, r.Init, r.InitHash, 1); err != nil {
			t.Fatal(err)
		}
		_ = r.L.ConfirmInit(ctx, r.Init, r.InitHash, 2)
		if r.Effects() != 1 {
			t.Fatalf("effects = %d after a second confirm with a newer attempt, want 1", r.Effects())
		}
	})
	t.Run("an initialized operation can be published once", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmPublish(ctx, r.Ready, r.PublishHash, 1); err != nil {
			t.Fatalf("ConfirmPublish = %v", err)
		}
		if p := r.Phase(r.Ready); p != PhaseRunning && p != PhasePublished {
			t.Fatalf("phase = %s", p)
		}
		_ = r.L.ConfirmPublish(ctx, r.Ready, r.PublishHash, 1)
		if r.Effects() != 1 {
			t.Fatalf("effects = %d, want 1", r.Effects())
		}
	})
	t.Run("a publish with a different hash is refused", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmPublish(ctx, r.Ready, r.InitHash, 1); err == nil {
			t.Fatal("ConfirmPublish accepted a different hash")
		}
		if r.Effects() != 0 {
			t.Fatal("a refused publish started work")
		}
	})
	t.Run("an operation that is not initialized is not published", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmPublish(ctx, r.Init, r.InitHash, 1); err == nil {
			t.Fatal("ConfirmPublish accepted an operation that is not ready")
		}
		if r.Effects() != 0 {
			t.Fatal("work started on an operation that is not ready")
		}
	})
	t.Run("reconcile is read-only and starts nothing", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ReconcileOperation(ctx, r.Init); err != nil {
			t.Fatalf("ReconcileOperation = %v", err)
		}
		if r.Phase(r.Init) != PhaseIdle || r.Effects() != 0 {
			t.Fatalf("a reconcile changed the operation: phase %s, effects %d", r.Phase(r.Init), r.Effects())
		}
		if err := r.L.ReconcileOperation(ctx, "op_unknown"); err == nil {
			t.Fatal("reconcile of an unknown operation was accepted")
		}
	})
	t.Run("a finished operation takes no new start", func(t *testing.T) {
		r := mk(t)
		if err := r.L.ConfirmInit(ctx, r.Init, r.InitHash, 1); err != nil {
			t.Fatal(err)
		}
		r.Finish(r.Init)
		_ = r.L.ConfirmInit(ctx, r.Init, r.InitHash, 2)
		if r.Phase(r.Init) != PhaseReady || r.Effects() != 1 {
			t.Fatalf("phase %s, effects %d after confirming a finished operation", r.Phase(r.Init), r.Effects())
		}
	})
}
