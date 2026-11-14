//go:build !windows

package platform

// exitObserver observes termination without reaping. Only process.run may reap
// the child. No SIGCHLD handler/foreign waiter may consume platform children.
//
// Identity proof: Start gives the child its own PGID == PID. The unreaped child
// reserves that PID even after exit. Kill holds killMu from the exited check
// through the group signal. run observes exit WITHOUT reaping, takes killMu
// (joining every pending signal), sets exited (rejecting every future signal),
// then releases the mutex and calls Cmd.Wait. Thus no signal overlaps identity
// release. Neither exit observation nor Cmd.Wait/pipe draining holds killMu;
// cancellation of a live or stopped child remains possible.
//
// Primitive setup/arming failures return no Process, kill the still-owned group
// before reaping, and close resources. Unexpected observation failures also kill
// before closing the signal gate and reaping; they are returned by Wait, never
// silently treated as successful exit observation. No weaker fallback is used.
// Windows retains its existing handle/job protocol and public Process contract.
type exitObserver interface {
	arm(pid int) error
	wait() error
	close()
}

// Per-start barriers used by package tests; nil in production. They observe the
// actual signal/reap boundary, without global hooks or replacing native effects.
type lifecycleHooks struct {
	beforeSignal func() error
	observed     func()
	beforeReap   func()
	reaped       func()
}
