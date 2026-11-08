package terminaltest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

const contractWait = 10 * time.Second

// PTYProgram tells the PTY contract what to start on the implementation under
// test. For the fake any Argv works; for the real ConPTY it is a shell.
type PTYProgram struct {
	Argv []string
	Dir  string
	Env  []string // must be non-nil
	// ExitInput, written to the terminal, makes the program exit with
	// ExitCode (for example "exit 3\r").
	ExitInput string
	ExitCode  int
}

// collector reads a PTY in the background, as callers must (docs/spikes.md B1).
type collector struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	err  error
	done chan struct{}
}

func collect(p platform.PTY) *collector {
	c := &collector{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		b := make([]byte, 1024)
		for {
			n, err := p.Read(b)
			c.mu.Lock()
			c.buf.Write(b[:n])
			if err != nil {
				c.err = err
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) waitFor(t *testing.T, sub string) {
	t.Helper()
	deadline := time.Now().Add(contractWait)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := strings.Contains(c.buf.String(), sub)
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("output never contained %q", sub)
}

func (c *collector) waitEnd(t *testing.T) error {
	t.Helper()
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	case <-time.After(contractWait):
		t.Fatal("Read never ended after the process exited")
		return nil
	}
}

// PTYContract is the contract of a PTYStarter. The fake and platform.StartPTY
// both run it (the real one only on Windows, where ConPTY lives; see
// contract_windows_test.go).
func PTYContract(t *testing.T, start terminal.PTYStarter, prog PTYProgram) {
	t.Helper()
	spec := func() platform.PTYSpec {
		return platform.PTYSpec{
			Spec: platform.Spec{Argv: prog.Argv, Dir: prog.Dir, Env: prog.Env},
			Size: platform.Size{Cols: 80, Rows: 24},
		}
	}
	begin := func(t *testing.T) platform.PTY {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		p, err := start(ctx, spec())
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	}

	t.Run("environment is required", func(t *testing.T) {
		s := spec()
		s.Env = nil
		if _, err := start(context.Background(), s); !errors.Is(err, platform.ErrNoEnv) {
			t.Fatalf("err = %v, want ErrNoEnv", err)
		}
	})
	t.Run("write echoes back", func(t *testing.T) {
		p := begin(t)
		c := collect(p)
		if _, err := p.Write([]byte("echo yggmarker\r")); err != nil {
			t.Fatal(err)
		}
		c.waitFor(t, "yggmarker")
	})
	t.Run("resize is accepted and a bad size is refused", func(t *testing.T) {
		p := begin(t)
		_ = collect(p)
		if err := p.Resize(platform.Size{Cols: 100, Rows: 30}); err != nil {
			t.Fatalf("resize: %v", err)
		}
		if err := p.Resize(platform.Size{Cols: 0, Rows: 0}); err == nil {
			t.Fatal("a 0x0 resize was accepted")
		}
	})
	t.Run("exit code and read ends after exit", func(t *testing.T) {
		p := begin(t)
		c := collect(p)
		if _, err := p.Write([]byte(prog.ExitInput)); err != nil {
			t.Fatal(err)
		}
		code, err := p.Wait()
		if err != nil || code != prog.ExitCode {
			t.Fatalf("Wait = %d, %v; want %d", code, err, prog.ExitCode)
		}
		if err := c.waitEnd(t); err == nil {
			t.Fatal("Read ended without an error")
		}
	})
	t.Run("close is idempotent and ends Wait", func(t *testing.T) {
		p := begin(t)
		c := collect(p)
		if err := p.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		waited := make(chan struct{})
		go func() { _, _ = p.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(contractWait):
			t.Fatal("Wait did not return after Close")
		}
		_ = c.waitEnd(t)
	})
	t.Run("a cancelled context stops the process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p, err := start(ctx, spec())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		_ = collect(p)
		cancel()
		waited := make(chan struct{})
		go func() { _, _ = p.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(contractWait):
			t.Fatal("Wait did not return after the context was cancelled")
		}
	})
}

// TokenSourceContract is the contract of a TokenSource: canonical 32-byte
// base64url tokens (so ct.ParseToken accepts them), all different.
func TokenSourceContract(t *testing.T, newSource func() terminal.TokenSource, draws int) {
	t.Helper()
	src := newSource()
	seen := make(map[string]bool, draws)
	for i := 0; i < draws; i++ {
		tok, err := src.New()
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if _, err := ct.ParseToken(tok); err != nil {
			t.Fatalf("draw %d gave a token the parser refuses: %v", i, err)
		}
		if seen[tok] {
			t.Fatalf("draw %d repeated a token", i)
		}
		seen[tok] = true
	}
}

// SocketPair is a Socket under test with the hooks to play the browser.
type SocketPair struct {
	Server terminal.Socket
	// Send plays a browser frame; Disconnect plays the browser leaving.
	Send       func(kind ct.FrameKind, data []byte)
	Disconnect func()
	// Output returns the binary data the server wrote, in order.
	Output func() string
	// Closed reports whether the server closed the socket, and the reason.
	Closed func() (bool, string)
}

// SocketContract checks ordering, close-once and write-after-close.
func SocketContract(t *testing.T, newPair func() SocketPair) {
	t.Helper()
	ctx := context.Background()
	t.Run("frames arrive in order", func(t *testing.T) {
		p := newPair()
		p.Send(ct.BinaryFrame, []byte("a"))
		p.Send(ct.TextFrame, []byte("b"))
		p.Send(ct.BinaryFrame, []byte("c"))
		for i, want := range []struct {
			k ct.FrameKind
			s string
		}{{ct.BinaryFrame, "a"}, {ct.TextFrame, "b"}, {ct.BinaryFrame, "c"}} {
			k, d, err := p.Server.Read(ctx)
			if err != nil || k != want.k || string(d) != want.s {
				t.Fatalf("frame %d = %v %q %v", i, k, d, err)
			}
		}
	})
	t.Run("writes keep their order", func(t *testing.T) {
		p := newPair()
		for _, s := range []string{"x", "y", "z"} {
			if err := p.Server.Write(ctx, []byte(s)); err != nil {
				t.Fatal(err)
			}
		}
		if got := p.Output(); got != "xyz" {
			t.Fatalf("output = %q", got)
		}
	})
	t.Run("read ends when the client leaves", func(t *testing.T) {
		p := newPair()
		p.Disconnect()
		if _, _, err := p.Server.Read(ctx); err == nil {
			t.Fatal("Read succeeded after the client left")
		}
	})
	t.Run("read honours the context", func(t *testing.T) {
		p := newPair()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, _, err := p.Server.Read(cctx); err == nil {
			t.Fatal("Read ignored a cancelled context")
		}
	})
	t.Run("close once, first reason wins, write after close errors", func(t *testing.T) {
		p := newPair()
		if err := p.Server.Close("first"); err != nil {
			t.Fatal(err)
		}
		if err := p.Server.Close("second"); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		if ok, why := p.Closed(); !ok || why != "first" {
			t.Fatalf("closed = %v %q", ok, why)
		}
		if err := p.Server.Write(ctx, []byte("late")); err == nil {
			t.Fatal("Write after Close succeeded")
		}
	})
}

// FoldersFixture is a Folders under test. It has repo "repo_a"
// (tracked), worktree "wt_a" of repo_a, repo "repo_b" and worktree "wt_b" of
// repo_b, all with different directories; remove untracks repo_a.
type FoldersFixture struct {
	Folders terminal.Folders
	// RemoveRepoA untracks repo_a.
	RemoveRepoA func()
}

// FoldersContract checks tracked-only resolution and re-checking.
func FoldersContract(t *testing.T, newFixture func() FoldersFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("a tracked repo and worktree resolve to different directories", func(t *testing.T) {
		fx := newFixture()
		fr, err := fx.Folders.Resolve(ctx, ct.Target{Repo: "repo_a"})
		if err != nil || fr != (ct.Folder{Repo: "repo_a"}) {
			t.Fatalf("repo: %v %v", fr, err)
		}
		fw, err := fx.Folders.Resolve(ctx, ct.Target{Worktree: "wt_a"})
		if err != nil || fw != (ct.Folder{Repo: "repo_a", Worktree: "wt_a"}) {
			t.Fatalf("worktree: %v %v", fw, err)
		}
		dr, err1 := fx.Folders.Dir(ctx, fr)
		dw, err2 := fx.Folders.Dir(ctx, fw)
		if err1 != nil || err2 != nil || dr == "" || dw == "" || dr == dw {
			t.Fatalf("dirs: %q %v / %q %v", dr, err1, dw, err2)
		}
	})
	t.Run("unknown IDs are not tracked", func(t *testing.T) {
		fx := newFixture()
		if _, err := fx.Folders.Resolve(ctx, ct.Target{Repo: "repo_nope"}); !errors.Is(err, terminal.ErrNotTracked) {
			t.Fatalf("repo err = %v", err)
		}
		if _, err := fx.Folders.Resolve(ctx, ct.Target{Worktree: "wt_nope"}); !errors.Is(err, terminal.ErrNotTracked) {
			t.Fatalf("worktree err = %v", err)
		}
	})
	t.Run("a worktree of another repo is refused", func(t *testing.T) {
		fx := newFixture()
		if _, err := fx.Folders.Dir(ctx, ct.Folder{Repo: "repo_a", Worktree: "wt_b"}); !errors.Is(err, terminal.ErrNotTracked) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a removed repo is untracked at the re-check", func(t *testing.T) {
		fx := newFixture()
		f, err := fx.Folders.Resolve(ctx, ct.Target{Repo: "repo_a"})
		if err != nil {
			t.Fatal(err)
		}
		fx.RemoveRepoA()
		if _, err := fx.Folders.Dir(ctx, f); !errors.Is(err, terminal.ErrNotTracked) {
			t.Fatalf("Dir after removal: %v", err)
		}
		if _, err := fx.Folders.Resolve(ctx, ct.Target{Worktree: "wt_a"}); !errors.Is(err, terminal.ErrNotTracked) {
			t.Fatalf("worktree of a removed repo: %v", err)
		}
	})
}

// AuditContract is the contract of an AuditSink: it stores what it is given,
// in order, and has no other input.
func AuditContract(t *testing.T, newSink func() (terminal.AuditSink, func() []terminal.AuditEntry)) {
	t.Helper()
	sink, entries := newSink()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want := []terminal.AuditEntry{
		{Action: ct.AuditOpen, Session: "ts_1", Folder: ct.Folder{Repo: "repo_a"}, At: at},
		{Action: ct.AuditClose, Session: "ts_1", Folder: ct.Folder{Repo: "repo_a", Worktree: "wt_1"}, Cause: ct.CauseShellExited, Code: 3, At: at.Add(time.Second)},
	}
	for _, e := range want {
		if err := sink.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	got := entries()
	if len(got) != len(want) {
		t.Fatalf("stored %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
