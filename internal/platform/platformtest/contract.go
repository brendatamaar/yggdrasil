package platformtest

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Commands tells the contract suite how to make the implementation under test
// do each thing. For the fake, see FakeCommands; for the real Start, argv of a
// helper program.
type Commands struct {
	// Env is a valid, non-nil environment for the commands.
	Env []string
	// Exit returns the argv of a program that exits with code.
	Exit func(code int) []string
	// Print returns the argv of a program that writes text and a newline to
	// stdout and exits 0. text is letters and digits only.
	Print func(text string) []string
	// Block returns the argv of a program that runs until it is killed.
	Block func() []string
	// Missing is the argv of a program that does not exist.
	Missing []string
	// MissingDir is a folder that does not exist, for Spec.Dir.
	MissingDir string
	// UnsafeBatch, if set, is the argv of a .cmd or .bat file with an argument
	// it must refuse (platform.ErrBatchArg). Leave nil where the implementation
	// has no batch files (the clause is skipped).
	UnsafeBatch []string
}

// FakeCommands are the Commands for a Fake.
func FakeCommands() Commands {
	return Commands{
		Env:         []string{"FAKE=1"},
		Exit:        func(code int) []string { return []string{ArgvPrefix, "exit", strconv.Itoa(code)} },
		Print:       func(text string) []string { return []string{ArgvPrefix, "print", text} },
		Block:       func() []string { return []string{ArgvPrefix, "block"} },
		Missing:     []string{"ygg-no-such-program"},
		MissingDir:  "fake-missing-dir",
		UnsafeBatch: []string{ArgvPrefix, "batch-unsafe"},
	}
}

const contractWait = 30 * time.Second

// ProcessContract checks that start behaves as platform.Start must. Run it
// against the fake and against the real platform.Start, so the fake can't
// drift from what it stands for.
func ProcessContract(t *testing.T, start platform.StartFunc, c Commands) {
	t.Helper()
	bg := context.Background()
	spec := func(argv []string) platform.Spec { return platform.Spec{Argv: argv, Env: c.Env} }
	wait := func(t *testing.T, p platform.Process) (int, error) {
		t.Helper()
		type result struct {
			code int
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			code, err := p.Wait()
			ch <- result{code, err}
		}()
		select {
		case r := <-ch:
			return r.code, r.err
		case <-time.After(contractWait):
			_ = p.Kill()
			t.Fatal("Wait did not return")
			return 0, nil
		}
	}
	mustStart := func(t *testing.T, ctx context.Context, argv []string) platform.Process {
		t.Helper()
		p, err := start(ctx, spec(argv))
		if err != nil {
			t.Fatalf("start %v: %v", argv, err)
		}
		t.Cleanup(func() { _ = p.Kill() })
		return p
	}

	t.Run("nil env is refused", func(t *testing.T) {
		_, err := start(bg, platform.Spec{Argv: c.Exit(0)})
		if !errors.Is(err, platform.ErrNoEnv) {
			t.Errorf("Start with nil env: %v, want ErrNoEnv", err)
		}
	})
	t.Run("empty argv is refused", func(t *testing.T) {
		if p, err := start(bg, platform.Spec{Env: c.Env}); err == nil {
			_ = p.Kill()
			t.Error("Start with no argv should fail")
		}
	})
	t.Run("cancelled context is refused", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		cancel()
		p, err := start(ctx, spec(c.Block()))
		if err == nil {
			_ = p.Kill()
			t.Fatal("Start with a cancelled context should fail")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
	t.Run("program not found", func(t *testing.T) {
		p, err := start(bg, spec(c.Missing))
		if err == nil {
			_ = p.Kill()
			t.Fatal("Start of a missing program should fail")
		}
		if !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
	t.Run("missing dir is ErrDirMissing", func(t *testing.T) {
		if c.MissingDir == "" {
			t.Skip("no MissingDir")
		}
		s := spec(c.Exit(0))
		s.Dir = c.MissingDir
		p, err := start(bg, s)
		if err == nil {
			_ = p.Kill()
			t.Fatal("Start in a missing folder should fail")
		}
		if !errors.Is(err, platform.ErrDirMissing) {
			t.Errorf("error = %v, want ErrDirMissing", err)
		}
		if errors.Is(err, platform.ErrNotFound) {
			t.Errorf("a missing folder is not a missing program: %v", err)
		}
	})
	t.Run("batch file with an unsafe argument is ErrBatchArg", func(t *testing.T) {
		if c.UnsafeBatch == nil {
			t.Skip("no batch files here")
		}
		p, err := start(bg, spec(c.UnsafeBatch))
		if err == nil {
			_ = p.Kill()
			t.Fatal("Start of a batch file with an unsafe argument should fail")
		}
		if !errors.Is(err, platform.ErrBatchArg) {
			t.Errorf("error = %v, want ErrBatchArg", err)
		}
	})
	t.Run("exit code zero", func(t *testing.T) {
		if code, err := wait(t, mustStart(t, bg, c.Exit(0))); err != nil || code != 0 {
			t.Errorf("Wait = %d, %v; want 0, nil", code, err)
		}
	})
	t.Run("non-zero exit is a result, not an error", func(t *testing.T) {
		if code, err := wait(t, mustStart(t, bg, c.Exit(7))); err != nil || code != 7 {
			t.Errorf("Wait = %d, %v; want 7, nil", code, err)
		}
	})
	t.Run("repeated Wait returns the same result", func(t *testing.T) {
		p := mustStart(t, bg, c.Exit(7))
		for i := range 3 {
			if code, err := wait(t, p); err != nil || code != 7 {
				t.Errorf("Wait #%d = %d, %v; want 7, nil", i+1, code, err)
			}
		}
	})
	t.Run("all stdout is delivered before Wait returns", func(t *testing.T) {
		text := strings.Repeat("0123456789", 600)
		var out lockedBuffer
		s := spec(c.Print(text))
		s.Stdout = &out
		p, err := start(bg, s)
		if err != nil {
			t.Fatal(err)
		}
		if code, err := wait(t, p); err != nil || code != 0 {
			t.Fatalf("Wait = %d, %v", code, err)
		}
		if got := strings.TrimRight(out.String(), "\r\n"); got != text {
			t.Errorf("stdout has %d bytes, want %d", len(got), len(text))
		}
	})
	t.Run("Kill is safe to repeat", func(t *testing.T) {
		p := mustStart(t, bg, c.Block())
		for i := range 3 {
			if err := p.Kill(); err != nil {
				t.Errorf("Kill #%d: %v", i+1, err)
			}
		}
		if code, err := wait(t, p); err != nil || code == 0 {
			t.Errorf("Wait after Kill = %d, %v; want non-zero, nil", code, err)
		}
		if err := p.Kill(); err != nil {
			t.Errorf("Kill after exit: %v", err)
		}
		done := mustStart(t, bg, c.Exit(0))
		_, _ = wait(t, done)
		if err := done.Kill(); err != nil {
			t.Errorf("Kill of a finished process: %v", err)
		}
	})
	t.Run("cancelling the context stops the process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		p := mustStart(t, ctx, c.Block())
		cancel()
		if code, err := wait(t, p); err != nil || code == 0 {
			t.Errorf("Wait after cancel = %d, %v; want non-zero, nil", code, err)
		}
	})
	t.Run("pid is positive", func(t *testing.T) {
		p := mustStart(t, bg, c.Exit(0))
		if p.Pid() <= 0 {
			t.Errorf("Pid = %d", p.Pid())
		}
		_, _ = wait(t, p)
	})
}

// lockedBuffer is a bytes.Buffer safe for a process writing while the test
// reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
