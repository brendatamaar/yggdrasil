//go:build !windows

package platform

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// Kill has passed its liveness check and owns permission to signal. Natural
// exit advances the real native observer while Kill is paused. At that exact
// boundary the signal mutex must remain held, exited must remain false, and
// the final reap must not have started. No timing/PID-reuse assumption is used.
func TestSignalRetainsIdentityUntilFinished(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	checked, resume, observed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	var signals, reaped atomic.Int32
	var p *process
	hooks := &lifecycleHooks{
		beforeSignal: func() error {
			signals.Add(1)
			close(checked)
			<-resume
			if reaped.Load() != 0 {
				return errors.New("signal attempted after final reap released identity")
			}
			return nil
		},
		observed: func() {
			<-checked
			if p.killMu.TryLock() {
				p.killMu.Unlock()
				t.Error("pending signal did not retain lifecycle mutex")
			}
			if p.exited.Load() || reaped.Load() != 0 {
				t.Error("identity released before pending signal finished")
			}
			close(observed)
		},
		beforeReap: func() {
			select {
			case <-resume:
			default:
				t.Error("final reap started before pending signal finished")
			}
			if !p.exited.Load() {
				t.Error("reap started with signal gate open")
			}
		},
		reaped: func() { reaped.Add(1) },
	}
	out := &terminationWriter{ready: make(chan struct{}), release: make(chan struct{})}
	var outputRelease sync.Once
	defer outputRelease.Do(func() { close(out.release) })
	proc, err := startObserved(context.Background(), Spec{Argv: []string{exe, "-test.run=^TestTerminationHelper$"}, Env: []string{"YGG_TERMINATION_TEST=7"}, Stdin: r, Stdout: out}, hooks, newExitObserver)
	if err != nil {
		t.Fatal(err)
	}
	p = proc.(*process)
	t.Cleanup(func() { release.Do(func() { close(resume) }); _ = p.Kill(); _, _ = p.Wait() })
	terminationSignal(t, out.ready)
	killed := make(chan struct{})
	go func() {
		defer close(killed)
		if err := p.Kill(); err != nil {
			t.Error(err)
		}
	}()
	terminationSignal(t, checked)
	if _, err := w.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	terminationSignal(t, observed)
	if reaped.Load() != 0 {
		t.Fatal("reaped during in-flight signal")
	}
	release.Do(func() { close(resume) })
	terminationSignal(t, killed)
	outputRelease.Do(func() { close(out.release) })
	code, err := p.Wait()
	if code != 7 || err != nil || WasTerminated(p) {
		t.Fatalf("natural failure changed: %d %v terminated=%v", code, err, WasTerminated(p))
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := p.Kill(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if signals.Load() != 1 || reaped.Load() != 1 {
		t.Fatalf("signals=%d reaps=%d", signals.Load(), reaped.Load())
	}
}

// Native effects remain real; only the observer's error boundary is injected.
type failingExitObserver struct {
	exitObserver
	armErr, waitErr error
	pid             int
	closed          bool
}

func (o *failingExitObserver) arm(pid int) error {
	o.pid = pid
	if o.armErr != nil {
		return o.armErr
	}
	return o.exitObserver.arm(pid)
}
func (o *failingExitObserver) wait() error { return o.waitErr }
func (o *failingExitObserver) close()      { o.exitObserver.close(); o.closed = true }

func TestExitObserverFailures(t *testing.T) {
	failure := errors.New("fictional observer failure")
	for _, stage := range []string{"prepare", "arm", "wait"} {
		t.Run(stage, func(t *testing.T) {
			var observer *failingExitObserver
			factory := func() (exitObserver, error) {
				if stage == "prepare" {
					return nil, failure
				}
				native, err := newExitObserver()
				if err != nil {
					return nil, err
				}
				observer = &failingExitObserver{exitObserver: native}
				if stage == "arm" {
					observer.armErr = failure
				} else {
					observer.waitErr = failure
				}
				return observer, nil
			}
			p, err := startObserved(context.Background(), helperSpec(t, "sleep"), nil, factory)
			if stage == "wait" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = p.Wait()
			} else if p != nil {
				t.Fatal("failed setup exposed a process")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("lost observation error: %v", err)
			}
			if observer != nil {
				if !observer.closed {
					t.Error("observer leaked")
				}
				var status syscall.WaitStatus
				_, err := syscall.Wait4(observer.pid, &status, syscall.WNOHANG, nil)
				if !errors.Is(err, syscall.ECHILD) {
					t.Errorf("child not reaped: %v", err)
				}
			}
		})
	}
}

func TestConcurrentLiveAndLateKill(t *testing.T) {
	p, err := Start(context.Background(), helperSpec(t, "sleep"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := p.Kill(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if code, err := p.Wait(); err != nil || code == 0 || !WasTerminated(p) {
		t.Fatalf("Kill: code=%d err=%v terminated=%v", code, err, WasTerminated(p))
	}
	for range 32 {
		wg.Go(func() {
			if err := p.Kill(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// Cmd.Wait also returns genuine IO errors independently of process status.
// The helper exits normally after its stdin closes; os/exec's stdin copier
// must preserve the reader error rather than converting it to (0, nil).
type failedProcessInput struct{ err error }

func (r failedProcessInput) Read([]byte) (int, error) { return 0, r.err }

func TestProcessPreservesWaitIOError(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("fictional stdin read failure")
	p, err := Start(context.Background(), Spec{
		Argv:  []string{exe, "-test.run=^TestTerminationHelper$"},
		Env:   []string{"YGG_TERMINATION_TEST=0"},
		Stdin: failedProcessInput{err: failure},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := p.Wait()
	if code != 0 || !errors.Is(err, failure) || WasTerminated(p) {
		t.Fatalf("Wait lost IO failure: code=%d err=%v terminated=%v", code, err, WasTerminated(p))
	}
}
