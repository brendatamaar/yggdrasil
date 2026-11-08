// Package terminaltest has the fakes of the terminal service's capabilities
// (PTY, token source, folders, socket, audit sink) and the contract suites
// that the fakes and the real implementations must both pass.
package terminaltest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

// ---- PTY ----

// FakePTY is an in-memory pseudo-terminal. Input written to it is echoed to
// the output, except a line "exit N" (CR or LF terminated), which ends the
// fake shell with code N. Tests drive it with Output and Exit.
type FakePTY struct {
	pid int

	mu      sync.Mutex
	written bytes.Buffer
	resizes []platform.Size
	closed  bool
	exited  bool
	code    int
	line    []byte

	out  chan []byte // output not yet read; closed when the shell ended
	done chan struct{}
	once sync.Once
	rbuf []byte
}

var fakePid atomic.Int64

// NewFakePTY returns a running fake shell.
func NewFakePTY() *FakePTY {
	return &FakePTY{pid: int(fakePid.Add(1)) + 1000, out: make(chan []byte, 1024), done: make(chan struct{})}
}

// Output makes the shell print s.
func (p *FakePTY) Output(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		p.out <- []byte(s)
	}
}

// Exit ends the shell with a code; Read drains the output and then fails.
func (p *FakePTY) Exit(code int) {
	p.once.Do(func() {
		p.mu.Lock()
		p.exited, p.code = true, code
		close(p.out)
		p.mu.Unlock()
		close(p.done)
	})
}

// Written is everything written to the terminal so far.
func (p *FakePTY) Written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written.String()
}

// Resizes lists the accepted resizes.
func (p *FakePTY) Resizes() []platform.Size {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]platform.Size(nil), p.resizes...)
}

// Closed reports whether Close was called.
func (p *FakePTY) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Read implements platform.PTY. After the shell ended and the output is
// drained it returns io.EOF.
func (p *FakePTY) Read(b []byte) (int, error) {
	if len(p.rbuf) == 0 {
		chunk, ok := <-p.out
		if !ok {
			return 0, io.EOF
		}
		p.rbuf = chunk
	}
	n := copy(b, p.rbuf)
	p.rbuf = p.rbuf[n:]
	return n, nil
}

// Write implements platform.PTY.
func (p *FakePTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	p.written.Write(b)
	var exit *int
	for _, c := range b {
		if c == '\r' || c == '\n' {
			if m := exitLine.FindSubmatch(p.line); m != nil {
				n, _ := strconv.Atoi(string(m[1]))
				exit = &n
			}
			p.line = p.line[:0]
			continue
		}
		p.line = append(p.line, c)
	}
	echo := append([]byte(nil), b...)
	p.out <- echo
	p.mu.Unlock()
	if exit != nil {
		p.Exit(*exit)
	}
	return len(b), nil
}

var exitLine = regexp.MustCompile(`^exit (\d+)$`)

// Resize implements platform.PTY.
func (p *FakePTY) Resize(s platform.Size) error {
	if s.Cols < 1 || s.Rows < 1 || s.Cols > 1000 || s.Rows > 1000 {
		return errors.New("fake pty: size out of range")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, s)
	return nil
}

// Pid implements platform.PTY.
func (p *FakePTY) Pid() int { return p.pid }

// Wait implements platform.PTY.
func (p *FakePTY) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, nil
}

// Close implements platform.PTY: kills the shell (code -1 if it still ran).
func (p *FakePTY) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.Exit(-1)
	return nil
}

var _ platform.PTY = (*FakePTY)(nil)

// Starter is a PTYStarter that hands out FakePTYs and records the specs.
type Starter struct {
	mu    sync.Mutex
	specs []platform.PTYSpec
	ptys  []*FakePTY
	// Err, if set, is returned instead of a PTY.
	Err error
	// Gate, if set, blocks every start until it is closed (or ctx ends).
	Gate chan struct{}
	// Entered counts the calls that reached Start (before the gate).
	Entered atomic.Int32
}

// Start implements terminal.PTYStarter, with the same checks as the real one.
func (s *Starter) Start(ctx context.Context, spec platform.PTYSpec) (platform.PTY, error) {
	s.Entered.Add(1)
	if spec.Env == nil {
		return nil, platform.ErrNoEnv
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("fake pty: Argv is empty")
	}
	if s.Gate != nil {
		select {
		case <-s.Gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	p := NewFakePTY()
	s.specs = append(s.specs, spec)
	s.ptys = append(s.ptys, p)
	context.AfterFunc(ctx, func() { _ = p.Close() })
	return p, nil
}

// Specs returns the specs of the started PTYs.
func (s *Starter) Specs() []platform.PTYSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]platform.PTYSpec(nil), s.specs...)
}

// PTYs returns the started PTYs.
func (s *Starter) PTYs() []*FakePTY {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*FakePTY(nil), s.ptys...)
}

// ---- tokens ----

// CounterTokens is a TokenSource that returns known, valid tokens: the n-th
// call encodes n as 32 bytes.
type CounterTokens struct {
	n atomic.Uint64
	// Err, if set, makes New fail.
	Err error
}

// New implements terminal.TokenSource.
func (c *CounterTokens) New() (string, error) {
	if c.Err != nil {
		return "", c.Err
	}
	var b [32]byte
	binary.BigEndian.PutUint64(b[24:], c.n.Add(1))
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ---- folders ----

// FakeFolders knows repos and worktrees by ID.
type FakeFolders struct {
	mu        sync.Mutex
	repos     map[core.RepoID]string
	worktrees map[core.WorktreeID]worktree
}

type worktree struct {
	repo core.RepoID
	dir  string
}

// NewFakeFolders returns an empty fake.
func NewFakeFolders() *FakeFolders {
	return &FakeFolders{repos: map[core.RepoID]string{}, worktrees: map[core.WorktreeID]worktree{}}
}

// AddRepo tracks a repo at dir.
func (f *FakeFolders) AddRepo(id core.RepoID, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[id] = dir
}

// AddWorktree tracks a worktree of a repo at dir.
func (f *FakeFolders) AddWorktree(id core.WorktreeID, repo core.RepoID, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.worktrees[id] = worktree{repo: repo, dir: dir}
}

// RemoveRepo untracks a repo (and with it its worktrees).
func (f *FakeFolders) RemoveRepo(id core.RepoID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.repos, id)
}

// Resolve implements terminal.Folders.
func (f *FakeFolders) Resolve(_ context.Context, t ct.Target) (ct.Folder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Repo != "" {
		if _, ok := f.repos[t.Repo]; !ok {
			return ct.Folder{}, terminal.ErrNotTracked
		}
		return ct.Folder{Repo: t.Repo}, nil
	}
	w, ok := f.worktrees[t.Worktree]
	if !ok {
		return ct.Folder{}, terminal.ErrNotTracked
	}
	if _, ok := f.repos[w.repo]; !ok {
		return ct.Folder{}, terminal.ErrNotTracked
	}
	return ct.Folder{Repo: w.repo, Worktree: t.Worktree}, nil
}

// Dir implements terminal.Folders.
func (f *FakeFolders) Dir(_ context.Context, fo ct.Folder) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, ok := f.repos[fo.Repo]
	if !ok {
		return "", terminal.ErrNotTracked
	}
	if fo.Worktree == "" {
		return dir, nil
	}
	w, ok := f.worktrees[fo.Worktree]
	if !ok || w.repo != fo.Repo {
		return "", terminal.ErrNotTracked
	}
	return w.dir, nil
}

// ---- audit ----

// MemAudit is an in-memory AuditSink.
type MemAudit struct {
	mu      sync.Mutex
	entries []terminal.AuditEntry
	// Err, if set, makes Record fail (the entry is not stored).
	Err error
	// Hook, if set, is called for every Record before storing; a non-nil
	// result fails that call. Calls counts every Record call.
	Hook  func(e terminal.AuditEntry) error
	calls int
}

// Calls is how many times Record was called, failed or not.
func (m *MemAudit) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Record implements terminal.AuditSink.
func (m *MemAudit) Record(_ context.Context, e terminal.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.Err != nil {
		return m.Err
	}
	if m.Hook != nil {
		if err := m.Hook(e); err != nil {
			return err
		}
	}
	m.entries = append(m.entries, e)
	return nil
}

// Entries returns the stored entries in order.
func (m *MemAudit) Entries() []terminal.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]terminal.AuditEntry(nil), m.entries...)
}

// ---- socket ----

// Frame is one client frame.
type Frame struct {
	Kind ct.FrameKind
	Data []byte
}

// MemSocket is the in-memory browser side of a session. The Service uses the
// terminal.Socket methods; the test plays the browser with Send, Recv and
// Disconnect.
type MemSocket struct {
	toServer chan Frame
	gone     chan struct{}
	goneOnce sync.Once

	mu       sync.Mutex
	output   [][]byte
	closed   bool
	reason   string
	closedCh chan struct{}

	// BlockWrites makes Write wait until ctx ends (a stalled browser).
	BlockWrites atomic.Bool
	// PanicOnRead makes Read panic once (a broken adapter).
	PanicOnRead atomic.Bool
}

// NewMemSocket returns an open socket.
func NewMemSocket() *MemSocket {
	return &MemSocket{toServer: make(chan Frame, 64), gone: make(chan struct{}), closedCh: make(chan struct{})}
}

// Send plays a frame from the browser.
func (m *MemSocket) Send(kind ct.FrameKind, data []byte) { m.toServer <- Frame{Kind: kind, Data: data} }

// Disconnect plays the browser going away.
func (m *MemSocket) Disconnect() { m.goneOnce.Do(func() { close(m.gone) }) }

// Output is all binary data written to the browser so far.
func (m *MemSocket) Output() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(bytes.Join(m.output, nil))
}

// Closed reports whether the server closed the socket, and why.
func (m *MemSocket) Closed() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed, m.reason
}

// Done is closed when the server closes the socket.
func (m *MemSocket) Done() <-chan struct{} { return m.closedCh }

// Read implements terminal.Socket.
func (m *MemSocket) Read(ctx context.Context) (ct.FrameKind, []byte, error) {
	if m.PanicOnRead.CompareAndSwap(true, false) {
		panic("memsocket: broken adapter")
	}
	select {
	case f := <-m.toServer:
		return f.Kind, f.Data, nil
	case <-m.gone:
		return 0, nil, io.EOF
	case <-m.closedCh:
		return 0, nil, io.ErrClosedPipe
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// Write implements terminal.Socket.
func (m *MemSocket) Write(ctx context.Context, data []byte) error {
	if m.BlockWrites.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.closedCh:
			return io.ErrClosedPipe
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return io.ErrClosedPipe
	}
	select {
	case <-m.gone:
		return io.ErrClosedPipe
	default:
	}
	m.output = append(m.output, append([]byte(nil), data...))
	return nil
}

// Close implements terminal.Socket: the first call wins.
func (m *MemSocket) Close(reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed, m.reason = true, reason
	close(m.closedCh)
	return nil
}

var (
	_ terminal.Socket      = (*MemSocket)(nil)
	_ terminal.TokenSource = (*CounterTokens)(nil)
	_ terminal.Folders     = (*FakeFolders)(nil)
	_ terminal.AuditSink   = (*MemAudit)(nil)
)
