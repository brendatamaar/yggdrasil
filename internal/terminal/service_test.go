package terminal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

const wait = 5 * time.Second

type fixture struct {
	svc     *terminal.Service
	starter *tt.Starter
	folders *tt.FakeFolders
	audit   *tt.MemAudit
	tokens  *tt.CounterTokens
	dir     string

	mu    sync.Mutex
	now   time.Time
	wakes []func()
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// fireWakes runs the wake-ups the service scheduled.
func (f *fixture) fireWakes() {
	f.mu.Lock()
	w := f.wakes
	f.wakes = nil
	f.mu.Unlock()
	for _, fn := range w {
		fn()
	}
}

func newFixture(t *testing.T, mutate ...func(*terminal.Config)) *fixture {
	t.Helper()
	f := &fixture{
		starter: &tt.Starter{}, folders: tt.NewFakeFolders(), audit: &tt.MemAudit{}, tokens: &tt.CounterTokens{},
		dir: t.TempDir(), now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	f.folders.AddRepo("repo_a", f.dir)
	f.folders.AddWorktree("wt_1", "repo_a", f.dir+"-wt")
	var n int
	cfg := terminal.Config{
		Folders: f.folders, Env: safety.NewEnvBuilder(), Audit: f.audit, Tokens: f.tokens,
		Start: f.starter.Start, Shell: func() []string { return []string{"fake-shell"} },
		Now: f.clock,
		After: func(_ time.Duration, fn func()) func() bool {
			f.mu.Lock()
			f.wakes = append(f.wakes, fn)
			f.mu.Unlock()
			return func() bool { return true }
		},
		NewID: func() core.TerminalSessionID { n++; return core.TerminalSessionID(fmt.Sprintf("ts_%d", n)) },
	}
	for _, m := range mutate {
		m(&cfg)
	}
	svc, err := terminal.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return f
}

// open issues a token for the repo and starts a session in a goroutine; it
// returns the socket and a channel with Run's result.
func (f *fixture) open(t *testing.T) (*tt.MemSocket, <-chan error) {
	t.Helper()
	tok, ttl, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if err != nil || ttl != 30*time.Second {
		t.Fatalf("Issue: %v %v", ttl, err)
	}
	sock := tt.NewMemSocket()
	done := make(chan error, 1)
	se, err := f.svc.Redeem(context.Background(), tok, ct.Size{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	go func() { done <- se.Run(context.Background(), sock) }()
	return sock, done
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func finished(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		t.Fatal("Run did not return")
		return nil
	}
}

func (f *fixture) pty(t *testing.T, i int) *tt.FakePTY {
	t.Helper()
	eventually(t, "a PTY to start", func() bool { return len(f.starter.PTYs()) > i })
	return f.starter.PTYs()[i]
}

func auditActions(a *tt.MemAudit) []ct.AuditAction {
	var out []ct.AuditAction
	for _, e := range a.Entries() {
		out = append(out, e.Action)
	}
	return out
}

func TestSessionLifecycleEndToEnd(t *testing.T) {
	f := newFixture(t)
	sock, done := f.open(t)
	p := f.pty(t, 0)

	// shell -> browser
	p.Output("hello from the shell\r\n")
	eventually(t, "output", func() bool { return strings.Contains(sock.Output(), "hello from the shell") })

	// browser -> shell, including a resize
	sock.Send(ct.BinaryFrame, []byte("ls\r"))
	sock.Send(ct.TextFrame, []byte(`{"type":"resize","cols":120,"rows":40}`))
	eventually(t, "keystrokes", func() bool { return p.Written() == "ls\r" })
	eventually(t, "resize", func() bool { return len(p.Resizes()) == 1 })
	if r := p.Resizes()[0]; r.Cols != 120 || r.Rows != 40 {
		t.Fatalf("resize = %+v", r)
	}

	// the shell exits
	sock.Send(ct.BinaryFrame, []byte("exit 3\r"))
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if ok, why := sock.Closed(); !ok || !strings.Contains(why, "code 3") {
		t.Fatalf("socket closed = %v %q", ok, why)
	}
	got := f.audit.Entries()
	if len(got) != 2 || got[0].Action != ct.AuditOpen || got[1].Action != ct.AuditClose || got[1].Cause != ct.CauseShellExited || got[1].Code != 3 {
		t.Fatalf("audit = %+v", got)
	}
	if got[0].Session != got[1].Session || got[0].Folder != (ct.Folder{Repo: "repo_a"}) {
		t.Fatalf("audit ids = %+v", got)
	}
}

func TestShellStartsInCleanEnvAtTheFolder(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-should-not-leak")
	t.Setenv("VIRTUAL_ENV", "/somewhere")
	f := newFixture(t)
	sock, done := f.open(t)
	p := f.pty(t, 0)
	spec := f.starter.Specs()[0]
	if spec.Dir != f.dir || len(spec.Argv) != 1 || spec.Argv[0] != "fake-shell" || spec.Size.Cols != 100 || spec.Size.Rows != 30 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Env == nil {
		t.Fatal("env is nil")
	}
	for _, kv := range spec.Env {
		if strings.Contains(kv, "glpat") || strings.HasPrefix(kv, "GITLAB_TOKEN") || strings.HasPrefix(kv, "VIRTUAL_ENV") {
			t.Fatalf("clean environment leaked %q", kv)
		}
	}
	p.Exit(0)
	_ = finished(t, done)
	_ = sock
}

func TestTokenIsSingleUse(t *testing.T) {
	f := newFixture(t)
	tok, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if err != nil {
		t.Fatal(err)
	}
	se, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	se.Abort()
	_, err = f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedReused || re.Message() == "" {
		t.Fatalf("second redeem err = %v", err)
	}
	if len(f.starter.PTYs()) != 0 {
		t.Fatal("a shell started for an aborted session")
	}
}

func TestExpiredAndUnknownTokensAreRefused(t *testing.T) {
	f := newFixture(t)
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	f.advance(31 * time.Second)
	_, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedExpired {
		t.Fatalf("expired: %v", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 43), "../../etc/passwd"} {
		if _, err := f.svc.Redeem(context.Background(), bad, terminal.DefaultSize); !errors.As(err, &re) || re.Reason != ct.RefusedUnknown {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestWakeRemovesExpiredTokensAndFreesPendingSlots(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < ct.MaxPending; i++ {
		if _, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedTooManyPending {
		t.Fatalf("over the limit: %v", err)
	}
	f.advance(31 * time.Second)
	f.fireWakes()
	if _, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"}); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestUntrackedFoldersAreRefused(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_x"}); !errors.Is(err, terminal.ErrNotTracked) {
		t.Fatalf("issue for an unknown repo: %v", err)
	}
	if _, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "a", Worktree: "b"}); err == nil {
		t.Fatal("both IDs accepted")
	}
	// removed between issue and redeem
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	f.folders.RemoveRepo("repo_a")
	_, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedNotTracked {
		t.Fatalf("redeem after removal: %v", err)
	}
	if len(f.starter.PTYs()) != 0 {
		t.Fatal("a shell started in an untracked folder")
	}
}

func TestSessionLimitAndOpenRefusalClosesSocket(t *testing.T) {
	f := newFixture(t)
	var dones []<-chan error
	for i := 0; i < ct.MaxSessions; i++ {
		_, d := f.open(t)
		dones = append(dones, d)
	}
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	sock := tt.NewMemSocket()
	err := f.svc.Open(context.Background(), tok, sock)
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedTooManySessions {
		t.Fatalf("fifth session: %v", err)
	}
	if ok, why := sock.Closed(); !ok || why != re.Message() {
		t.Fatalf("socket = %v %q", ok, why)
	}
	for i := range dones {
		f.pty(t, i).Exit(0)
	}
	for _, d := range dones {
		_ = finished(t, d)
	}
}

func TestClientCloseKillsShellAndAudits(t *testing.T) {
	f := newFixture(t)
	sock, done := f.open(t)
	p := f.pty(t, 0)
	eventually(t, "open audit", func() bool { return len(f.audit.Entries()) == 1 })
	sock.Disconnect()
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if !p.Closed() {
		t.Fatal("PTY was not closed")
	}
	got := f.audit.Entries()
	if len(got) != 2 || got[1].Action != ct.AuditClose || got[1].Cause != ct.CauseClientClosed {
		t.Fatalf("audit = %+v", got)
	}
}

func TestUserCloseButton(t *testing.T) {
	f := newFixture(t)
	sock, done := f.open(t)
	f.pty(t, 0)
	eventually(t, "open audit", func() bool { return len(f.audit.Entries()) == 1 })
	se := f.svc
	se.CloseSession("ts_1")
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if got := f.audit.Entries(); len(got) != 2 || got[1].Cause != ct.CauseUserClosed {
		t.Fatalf("audit = %+v", got)
	}
	if ok, why := sock.Closed(); !ok || !strings.Contains(why, "closed") {
		t.Fatalf("socket = %v %q", ok, why)
	}
}

func TestCloseWhileShellIsStarting(t *testing.T) {
	gate := make(chan struct{})
	f := newFixture(t)
	f.starter.Gate = gate
	_, done := f.open(t)
	eventually(t, "the shell start", func() bool { return f.starter.Entered.Load() == 1 })
	f.svc.CloseSession("ts_1") // asked for before the PTY exists
	close(gate)
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.starter.PTYs() {
		if !p.Closed() {
			t.Fatal("a shell started during a close was left running")
		}
	}
	got := f.audit.Entries()
	if len(got) != 2 || got[0].Action != ct.AuditOpen || got[1].Action != ct.AuditClose {
		t.Fatalf("audit = %+v", got)
	}
}

func TestOpenFailureIsAudited(t *testing.T) {
	f := newFixture(t)
	f.starter.Err = errors.New("no shell")
	sock, done := f.open(t)
	if err := finished(t, done); err == nil {
		t.Fatal("Run hid the start failure")
	}
	if ok, why := sock.Closed(); !ok || why == "" || strings.Contains(why, f.dir) {
		t.Fatalf("socket = %v %q", ok, why)
	}
	got := f.audit.Entries()
	if len(got) != 2 || got[1].Action != ct.AuditOpenFailed || got[1].Cause != ct.CauseOpenFailed {
		t.Fatalf("audit = %+v", got)
	}
	// the slot is free again
	f.starter.Err = nil
	_, d := f.open(t)
	f.pty(t, 0).Exit(0)
	_ = finished(t, d)
}

func TestAbortBeforeRunIsAudited(t *testing.T) {
	f := newFixture(t)
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	se, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	se.Abort()
	se.Abort()
	if err := se.Run(context.Background(), tt.NewMemSocket()); !errors.Is(err, terminal.ErrSessionGone) {
		t.Fatalf("Run after Abort: %v", err)
	}
	if got := auditActions(f.audit); len(got) != 2 || got[1] != ct.AuditOpenFailed {
		t.Fatalf("audit = %v", got)
	}
}

func TestAuditHoldsOnlyIDsCauseAndCode(t *testing.T) {
	f := newFixture(t)
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	sock := tt.NewMemSocket()
	se, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- se.Run(context.Background(), sock) }()
	p := f.pty(t, 0)
	sock.Send(ct.BinaryFrame, []byte("echo SECRET-KEYSTROKES\r"))
	eventually(t, "keystrokes", func() bool { return strings.Contains(p.Written(), "SECRET") })
	sock.Send(ct.BinaryFrame, []byte("exit 0\r"))
	_ = finished(t, done)

	raw, _ := json.Marshal(f.audit.Entries())
	for _, banned := range []string{"SECRET", "echo", "exit 0", f.dir, tok, string(ct.HashToken(tok)), "fake-shell"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("audit entries contain %q: %s", banned, raw)
		}
	}
	if len(f.audit.Entries()) != 2 {
		t.Fatalf("audit = %s", raw)
	}
}

func TestInvalidClientFrameEndsTheSession(t *testing.T) {
	f := newFixture(t)
	sock, done := f.open(t)
	f.pty(t, 0)
	sock.Send(ct.TextFrame, []byte(`{"type":"exec","cmd":"rm -rf /"}`))
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if !f.starter.PTYs()[0].Closed() {
		t.Fatal("shell kept running after a protocol violation")
	}
}

func TestSlowClientDoesNotBlockTheTerminal(t *testing.T) {
	f := newFixture(t, func(c *terminal.Config) { c.OutputQueue = 4; c.WriteTimeout = 20 * time.Millisecond })
	sock, done := f.open(t)
	sock.BlockWrites.Store(true)
	p := f.pty(t, 0)
	for i := 0; i < 200; i++ {
		p.Output(strings.Repeat("x", 100))
	}
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if !p.Closed() {
		t.Fatal("slow client's shell kept running")
	}
	if ok, why := sock.Closed(); !ok || !strings.Contains(why, "keep up") {
		t.Fatalf("socket = %v %q", ok, why)
	}
}

func TestPanicInSocketIsRecovered(t *testing.T) {
	f := newFixture(t)
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	sock := tt.NewMemSocket()
	sock.PanicOnRead.Store(true)
	se, _ := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	done := make(chan error, 1)
	go func() { done <- se.Run(context.Background(), sock) }()
	if err := finished(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !f.pty(t, 0).Closed() {
		t.Fatal("shell kept running after a panic")
	}
	if got := f.audit.Entries(); len(got) != 2 || got[1].Cause != ct.CauseClientClosed {
		t.Fatalf("audit = %+v", got)
	}
}

func TestCloseDrainsAllSessions(t *testing.T) {
	f := newFixture(t)
	var dones []<-chan error
	for i := 0; i < 2; i++ {
		_, d := f.open(t)
		dones = append(dones, d)
		f.pty(t, i)
	}
	// a third session reserved but never started
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if _, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := f.svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, d := range dones {
		_ = finished(t, d)
	}
	for _, p := range f.starter.PTYs() {
		if !p.Closed() {
			t.Fatal("a shell survived Close")
		}
	}
	closes := 0
	for _, e := range f.audit.Entries() {
		if e.Action == ct.AuditClose {
			if e.Cause != ct.CauseAppShutdown {
				t.Errorf("cause = %v", e.Cause)
			}
			closes++
		}
	}
	if closes != 3 {
		t.Fatalf("%d close audits, want 3", closes)
	}
	if _, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"}); err == nil {
		t.Fatal("Issue worked after Close")
	}
	if err := f.svc.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// The open audit is written before any shell exists; if it cannot be, the
// open is refused (invariant 8).
func TestOpenAuditFailureRefusesTheOpen(t *testing.T) {
	f := newFixture(t)
	f.audit.Hook = func(e terminal.AuditEntry) error {
		if e.Action == ct.AuditOpen {
			return errors.New("disk full")
		}
		return nil
	}
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	sock := tt.NewMemSocket()
	res := make(chan error, 1)
	go func() { res <- f.svc.Open(context.Background(), tok, sock) }()
	var err error
	select {
	case err = <-res:
	case <-time.After(2 * time.Second):
		t.Fatal("Open ran a session although the open could not be audited")
	}
	if !errors.Is(err, terminal.ErrAuditUnavailable) {
		t.Fatalf("Open = %v", err)
	}
	if f.starter.Entered.Load() != 0 {
		t.Fatal("a shell was started although the open could not be audited")
	}
	if ok, why := sock.Closed(); !ok || !strings.Contains(why, "audit log") {
		t.Fatalf("socket = %v %q", ok, why)
	}
	got := f.audit.Entries()
	if len(got) != 1 || got[0].Action != ct.AuditOpenFailed {
		t.Fatalf("audit = %+v", got)
	}
	// the slot is free: four sessions still fit
	f.audit.Hook = nil
	var dones []<-chan error
	for i := 0; i < ct.MaxSessions; i++ {
		_, d := f.open(t)
		dones = append(dones, d)
	}
	for i := range dones {
		f.pty(t, i).Exit(0)
	}
	for _, d := range dones {
		_ = finished(t, d)
	}
}

func TestCloseAuditIsRetriedThenLogged(t *testing.T) {
	var logs strings.Builder
	f := newFixture(t, func(c *terminal.Config) {
		c.AuditBackoff = time.Millisecond
		c.Log = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))
	})
	failures := 2
	f.audit.Hook = func(e terminal.AuditEntry) error {
		if e.Action == ct.AuditClose && failures > 0 {
			failures--
			return errors.New("busy")
		}
		return nil
	}
	_, done := f.open(t)
	f.pty(t, 0).Exit(0)
	_ = finished(t, done)
	if got := auditActions(f.audit); len(got) != 2 || got[1] != ct.AuditClose {
		t.Fatalf("close audit lost after transient failures: %v", got)
	}

	// a store that stays down: bounded attempts, then a log line with IDs only
	before := f.audit.Calls()
	f.audit.Hook = func(e terminal.AuditEntry) error {
		if e.Action == ct.AuditClose {
			return errors.New("down")
		}
		return nil
	}
	_, done = f.open(t)
	f.pty(t, 1).Exit(0)
	_ = finished(t, done)
	if n := f.audit.Calls() - before; n != 1+4 { // the open, then 4 attempts at the close
		t.Fatalf("audit calls = %d, want 5", n)
	}
	if out := logs.String(); !strings.Contains(out, "audit write failed") || !strings.Contains(out, "ts_2") || strings.Contains(out, f.dir) {
		t.Fatalf("log = %q", out)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// A redeem whose socket never comes must not hold a slot or block Close.
func TestUnclaimedSessionExpires(t *testing.T) {
	f := newFixture(t)
	var unclaimed []*terminal.Session
	for i := 0; i < ct.MaxSessions; i++ {
		tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
		se, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
		if err != nil {
			t.Fatal(err)
		}
		unclaimed = append(unclaimed, se)
	}
	tok, _, _ := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	_, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	var re *terminal.RefusedError
	if !errors.As(err, &re) || re.Reason != ct.RefusedTooManySessions {
		t.Fatalf("full: %v", err)
	}

	f.advance(ct.OpenDeadline)
	f.fireWakes()

	// the slots are free again (a fresh token: the one above was burned)
	tok, _, err = f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if err != nil {
		t.Fatal(err)
	}
	se, err := f.svc.Redeem(context.Background(), tok, terminal.DefaultSize)
	if err != nil {
		t.Fatalf("after the deadline: %v", err)
	}
	if err := unclaimed[0].Run(context.Background(), tt.NewMemSocket()); !errors.Is(err, terminal.ErrSessionGone) {
		t.Fatalf("Run of an expired session: %v", err)
	}
	failed := 0
	for _, a := range auditActions(f.audit) {
		if a == ct.AuditOpenFailed {
			failed++
		}
	}
	if failed != ct.MaxSessions {
		t.Fatalf("%d open_failed audits, want %d", failed, ct.MaxSessions)
	}

	// Close does not wait for a session nobody started
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_ = se
	if err := f.svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRealTokenSourceContract(t *testing.T) {
	tt.TokenSourceContract(t, func() terminal.TokenSource { return terminal.CryptoTokens{} }, 10000)
}

func TestNewRequiresPorts(t *testing.T) {
	if _, err := terminal.New(terminal.Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
}

// A close audit that cannot be written is kept and written later, at the
// latest by Close (invariant 8).
func TestFailedCloseAuditIsRetriedLater(t *testing.T) {
	f := newFixture(t)
	var down atomic.Bool
	f.audit.Hook = func(e terminal.AuditEntry) error {
		if e.Action == ct.AuditClose && down.Load() {
			return errors.New("disk full")
		}
		return nil
	}
	sock, done := f.open(t)
	f.pty(t, 0)
	eventually(t, "open audit", func() bool { return len(f.audit.Entries()) == 1 })
	down.Store(true)
	sock.Disconnect()
	if err := finished(t, done); err != nil {
		t.Fatal(err)
	}
	if got := f.audit.Entries(); len(got) != 1 {
		t.Fatalf("close audit written although the sink was down: %+v", got)
	}
	down.Store(false) // the sink recovers; shutdown makes the last attempt
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.audit.Entries()
	if len(got) != 2 || got[1].Action != ct.AuditClose {
		t.Fatalf("audit = %+v", got)
	}
}
