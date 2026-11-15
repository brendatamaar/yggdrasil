//go:build !windows

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func ptyHelperSpec(t *testing.T) PTYSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return PTYSpec{Spec: Spec{Argv: []string{exe, "-test.run=^TestTerminationHelper$"}, Env: []string{"YGG_TERMINATION_TEST=7"}}, Size: Size{Cols: 80, Rows: 24}}
}

func readyPTY(t *testing.T, p PTY) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		b := make([]byte, 5)
		if _, err := io.ReadFull(p, b); err != nil {
			t.Error(err)
		}
	}()
	terminationSignal(t, ready)
}

// Both boundaries use the real OS observer. The signal hook refuses a syscall
// after reap even under an intentionally unsafe negative-control mutation.
func TestPTYSignalRetainsIdentity(t *testing.T) {
	checked, resume, observed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	var reaped, kills, hangups atomic.Int32
	var p *unixPTY
	hooks := &ptyLifecycleHooks{
		beforeSignal: func(sig syscall.Signal) error {
			if sig == syscall.SIGKILL {
				kills.Add(1)
				close(checked)
				<-resume
			} else {
				hangups.Add(1)
			}
			if reaped.Load() != 0 {
				t.Error("signal attempted after reap")
				return errors.New("refuse post-reap syscall")
			}
			return nil
		},
		observed: func() {
			<-checked
			if p.killMu.TryLock() {
				p.killMu.Unlock()
				t.Error("pending signal did not retain mutex")
			}
			if reaped.Load() != 0 {
				t.Error("identity released during pending signal")
			}
			close(observed)
		},
		beforeReap: func() {
			select {
			case <-resume:
			default:
				t.Error("reap before pending signal finished")
			}
			p.killMu.Lock()
			closed := p.exited
			p.killMu.Unlock()
			if !closed || hangups.Load() != 1 {
				t.Error("reap before gate closure and final hangup")
			}
		},
		reaped: func() { reaped.Add(1) },
	}
	proc, err := startPTYObserved(context.Background(), ptyHelperSpec(t), hooks, newExitObserver)
	if err != nil {
		t.Fatal(err)
	}
	p = proc.(*unixPTY)
	t.Cleanup(func() { release.Do(func() { close(resume) }); _ = p.Close() })
	readyPTY(t, p)
	killed := make(chan struct{})
	go func() {
		defer close(killed)
		if err := p.kill(); err != nil {
			t.Error(err)
		}
	}()
	terminationSignal(t, checked)
	if _, err := p.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	terminationSignal(t, observed)
	if reaped.Load() != 0 {
		t.Error("reaped during pending signal")
	}
	release.Do(func() { close(resume) })
	terminationSignal(t, killed)
	if code, err := p.Wait(); code != 7 || err != nil {
		t.Fatalf("Wait=%d,%v", code, err)
	}
	for range 8 {
		if err := p.kill(); err != nil {
			t.Error(err)
		}
	}
	if kills.Load() != 1 || hangups.Load() != 1 || reaped.Load() != 1 {
		t.Fatalf("kills=%d hangups=%d reaps=%d", kills.Load(), hangups.Load(), reaped.Load())
	}
}

func TestPTYNaturalHangupBeforeReap(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	var reaped, hangups atomic.Int32
	hooks := &ptyLifecycleHooks{
		beforeSignal: func(sig syscall.Signal) error {
			if sig != syscall.SIGHUP {
				t.Error("unexpected cancellation")
			}
			hangups.Add(1)
			close(entered)
			<-resume
			if reaped.Load() != 0 {
				t.Error("natural hangup after reap")
				return errors.New("refuse post-reap syscall")
			}
			return nil
		},
		beforeReap: func() {
			if hangups.Load() != 1 {
				t.Error("reap before natural hangup")
			}
		},
		reaped: func() { reaped.Add(1) },
	}
	proc, err := startPTYObserved(context.Background(), ptyHelperSpec(t), hooks, newExitObserver)
	if err != nil {
		t.Fatal(err)
	}
	p := proc.(*unixPTY)
	t.Cleanup(func() { release.Do(func() { close(resume) }); _ = p.Close() })
	readyPTY(t, p)
	if _, err := p.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	terminationSignal(t, entered)
	if reaped.Load() != 0 {
		t.Error("reaped before hangup completed")
	}
	release.Do(func() { close(resume) })
	if code, err := p.Wait(); code != 7 || err != nil {
		t.Fatalf("Wait=%d,%v", code, err)
	}
	if err := p.kill(); err != nil {
		t.Fatal(err)
	}
	if hangups.Load() != 1 || reaped.Load() != 1 {
		t.Fatal("unexpected signal/reap count")
	}
}

func TestPTYObserverFailures(t *testing.T) {
	failure := errors.New("fictional PTY observer failure")
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
			p, err := startPTYObserved(context.Background(), ptyHelperSpec(t), nil, factory)
			if stage == "wait" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = p.Wait()
				if e := p.Close(); e != nil {
					t.Error(e)
				}
			} else if p != nil {
				t.Fatal("setup published a PTY")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("lost error: %v", err)
			}
			if observer != nil {
				if !observer.closed {
					t.Error("observer leaked")
				}
				var status syscall.WaitStatus
				if _, err := syscall.Wait4(observer.pid, &status, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
					t.Errorf("child not reaped: %v", err)
				}
			}
		})
	}
}

func TestPTYConcurrentCloseCancelWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := ptyHelperSpec(t)
	spec.TaskID = "fictional-pty-lifecycle"
	p, err := StartPTY(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	readyPTY(t, p)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		})
		wg.Go(cancel)
		wg.Go(func() {
			if err := KillTask(spec.TaskID); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if code, err := p.Wait(); code == 0 || err != nil {
				t.Errorf("Wait=%d,%v", code, err)
			}
		})
	}
	wg.Wait()
	if _, err := p.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("master not closed: %v", err)
	}
	taskReg.mu.Lock()
	defer taskReg.mu.Unlock()
	if len(taskReg.m[spec.TaskID]) != 0 {
		t.Error("task registration leaked")
	}
}

func TestPTYStoppedChildCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := StartPTY(ctx, ptyHelperSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	readyPTY(t, p)
	if err := syscall.Kill(p.Pid(), syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// Wait for an actual stop status; this does not consume termination status.
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(p.Pid(), &status, syscall.WUNTRACED, nil); err != nil || !status.Stopped() {
		t.Fatalf("stop=%v %v", status, err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Wait(); err != nil {
			t.Error(err)
		}
	}()
	terminationSignal(t, done)
}

type fastPTYObserver struct{ exitObserver }

func (o fastPTYObserver) arm(pid int) error {
	// Delay native attachment until the child is already an unreaped zombie.
	// Native wait observation is used instead of a timing assumption.
	native, err := newExitObserver()
	if err != nil {
		return err
	}
	defer native.close()
	if err = native.arm(pid); err != nil {
		return err
	}
	if err = native.wait(); err != nil {
		return err
	}
	return o.exitObserver.arm(pid)
}
func TestPTYFastExitAttach(t *testing.T) {
	spec := ptyHelperSpec(t)
	spec.Argv = []string{"/bin/sh", "-c", "exit 3"}
	p, err := startPTYObserved(context.Background(), spec, nil, func() (exitObserver, error) { o, e := newExitObserver(); return fastPTYObserver{o}, e })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if code, err := p.Wait(); code != 3 || err != nil {
		t.Fatalf("Wait=%d,%v", code, err)
	}
}

// Release does not reap on Unix. It makes Cmd.Wait return a genuine error;
// the test alone then collects the retained zombie, with the signal gate closed.
func TestPTYPreservesRealWaitError(t *testing.T) {
	var p *unixPTY
	published := make(chan struct{})
	hooks := &ptyLifecycleHooks{beforeReap: func() {
		<-published
		if err := p.cmd.Process.Release(); err != nil {
			t.Error(err)
		}
	}}
	proc, err := startPTYObserved(context.Background(), ptyHelperSpec(t), hooks, newExitObserver)
	if err != nil {
		t.Fatal(err)
	}
	p = proc.(*unixPTY)
	pid := p.Pid()
	close(published)
	defer func() { var status syscall.WaitStatus; _, _ = syscall.Wait4(pid, &status, 0, nil); _ = p.Close() }()
	readyPTY(t, p)
	if _, err := p.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err == nil {
		t.Fatal("lost actual Cmd.Wait error")
	}
	if err := p.kill(); err != nil {
		t.Fatal(err)
	}
}

// A real background child retains the terminal group when the leader exits.
func TestPTYTreeHelper(t *testing.T) {
	mode := os.Getenv("YGG_PTY_TREE")
	if mode == "" {
		return
	}
	if mode == "child" {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		_, _ = io.WriteString(os.Stdout, "ready")
		<-hup
		if err := writeFileAtomic(os.Getenv("YGG_PTY_RESULT"), "hangup"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(exe, "-test.run=^TestPTYTreeHelper$")
	child.Env = []string{"YGG_PTY_TREE=child", "YGG_PTY_RESULT=" + os.Getenv("YGG_PTY_RESULT")}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
	os.Exit(0)
}

func TestPTYBackgroundHangup(t *testing.T) {
	result := filepath.Join(t.TempDir(), "hangup")
	spec := ptyHelperSpec(t)
	spec.Argv[1] = "-test.run=^TestPTYTreeHelper$"
	spec.Env = []string{"YGG_PTY_TREE=parent", "YGG_PTY_RESULT=" + result}
	p, err := StartPTY(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	readyPTY(t, p)
	if _, err := p.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); code != 0 || err != nil {
		t.Fatalf("Wait=%d,%v", code, err)
	}
	if got := waitFile(t, result); got != "hangup" {
		t.Fatalf("background child: %q", got)
	}
}

func TestPTYCloseTimeoutClosesMaster(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	hooks := &ptyLifecycleHooks{beforeReap: func() { close(entered); <-resume }}
	p, err := startPTYObserved(context.Background(), ptyHelperSpec(t), hooks, newExitObserver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release.Do(func() { close(resume) }); _, _ = p.Wait(); _ = p.Close() })
	readyPTY(t, p)
	if _, err := p.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	terminationSignal(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case err := <-closed:
		if err == nil {
			t.Error("missing close timeout")
		}
	case <-time.After(closeTimeout + 2*time.Second):
		t.Fatal("Close exceeded bound")
	}
	if _, err := p.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("master leaked on timeout: %v", err)
	}
	release.Do(func() { close(resume) })
	if code, err := p.Wait(); code != 7 || err != nil {
		t.Fatalf("Wait=%d,%v", code, err)
	}
}
