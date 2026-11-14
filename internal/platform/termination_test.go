package platform

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A portable helper: no shells, network, hosts or inherited environment.
func TestTerminationHelper(t *testing.T) {
	mode := os.Getenv("YGG_TERMINATION_TEST")
	if mode == "" {
		return
	}
	_, _ = io.WriteString(os.Stdout, "ready")
	if mode == "block" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	// The parent obtains any OS wait handle before permitting natural exit.
	_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
	code, _ := strconv.Atoi(mode)
	os.Exit(code)
}

type terminationWriter struct {
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *terminationWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	<-w.release
	return len(b), nil
}

func terminationSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("helper synchronization timed out")
	}
}

func TestProcessTerminationEvidence(t *testing.T) {
	for _, mode := range []string{"block", "0", "7"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
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
			out := &terminationWriter{ready: make(chan struct{}), release: make(chan struct{})}
			p, err := Start(ctx, Spec{Argv: []string{exe, "-test.run=^TestTerminationHelper$"}, Env: []string{"YGG_TERMINATION_TEST=" + mode}, Stdin: r, Stdout: out})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Kill(); _, _ = p.Wait() })
			var release sync.Once
			defer release.Do(func() { close(out.release) })
			terminationSignal(t, out.ready)
			if mode != "block" {
				// Prove the main process exited while output is still blocked.
				waitTerminationExit(t, p, func() {
					if _, err := w.Write([]byte{1}); err != nil {
						t.Fatal(err)
					}
				})
			}
			if mode == "block" {
				// Real context callback must terminate the live process.
				cancel()
			} else if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(out.release) })
			code, err := p.Wait()
			if err != nil || WasTerminated(p) != (mode == "block") || p.Truncated() {
				t.Fatalf("termination evidence: mode=%s code=%d err=%v terminated=%v cut=%v", mode, code, err, WasTerminated(p), p.Truncated())
			}
			if mode != "block" {
				want, _ := strconv.Atoi(mode)
				if code != want {
					t.Fatalf("natural exit changed: %d want %d", code, want)
				}
			} else if code == 0 {
				t.Fatal("killed helper reported success")
			}
			if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			if WasTerminated(p) != (mode == "block") {
				t.Fatal("late Kill changed evidence")
			}
		})
	}
}

type unknownTerminationProcess struct{}

func (unknownTerminationProcess) Pid() int           { return 1 }
func (unknownTerminationProcess) Wait() (int, error) { return -1, nil }
func (unknownTerminationProcess) Kill() error        { return nil }
func (unknownTerminationProcess) Truncated() bool    { return true }
func TestWasTerminatedUnknownFailsClosed(t *testing.T) {
	p := unknownTerminationProcess{}
	_ = p.Kill()
	if WasTerminated(p) {
		t.Fatal("unknown process supplied termination evidence")
	}
}
