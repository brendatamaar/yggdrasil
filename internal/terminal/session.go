package terminal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// ErrSessionGone is returned by Run when the session was already closed or
// aborted before it started.
var ErrSessionGone = errors.New("terminal: session is no longer available")

const (
	// drainWait is how long we wait for the last output after the shell
	// exited before forcing the terminal closed (a background child can keep
	// the terminal open).
	drainWait = 2 * time.Second
	// closeWait bounds the wait for the output to end after Close.
	closeWait = 5 * time.Second
	readChunk = 4096
)

type liveState int

const (
	idle liveState = iota
	running
	abandoned
)

// live is the shell's record of one session: the PTY handle and whether a
// close was asked for before the PTY existed.
type live struct {
	svc    *Service
	id     core.TerminalSessionID
	folder ct.Folder
	size   ct.Size

	mu       sync.Mutex
	state    liveState
	pty      platform.PTY
	closeReq bool
}

// claim marks the session as running; false if it was abandoned or already
// claimed.
func (l *live) claim() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != idle {
		return false
	}
	l.state = running
	return true
}

// abandonIfIdle marks a never-started session as abandoned.
func (l *live) abandonIfIdle() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != idle {
		return false
	}
	l.state = abandoned
	return true
}

// attach stores the PTY. It returns false if a close was asked for already;
// the caller then closes the PTY.
func (l *live) attach(p platform.PTY) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pty = p
	return !l.closeReq
}

func (l *live) resize(sz ct.Size) {
	l.mu.Lock()
	p := l.pty
	l.mu.Unlock()
	if p != nil {
		if err := p.Resize(platform.Size{Cols: sz.Cols, Rows: sz.Rows}); err != nil {
			l.svc.cfg.Log.Warn("terminal resize failed", "session", l.id, "err", err)
		}
	}
}

// requestClose implements ClosePTY. It is idempotent.
func (l *live) requestClose() {
	if l.abandonIfIdle() {
		// Nobody started the session: finish it here.
		l.svc.interpret(l.svc.step(ct.ShellExited{Session: l.id, Code: -1, Now: l.svc.cfg.Now()}))
		l.svc.release(l.id)
		return
	}
	l.mu.Lock()
	l.closeReq = true
	p := l.pty
	l.mu.Unlock()
	if p != nil {
		_ = p.Close() // idempotent; kills the process tree
	}
}

func (s *Service) release(id core.TerminalSessionID) {
	s.mu.Lock()
	delete(s.live, id)
	s.mu.Unlock()
	s.wg.Done()
}

// Session is a redeemed token: a reserved session waiting for its socket.
type Session struct{ l *live }

// ID is the session ID (also what CloseSession takes).
func (se *Session) ID() core.TerminalSessionID { return se.l.id }

// Abort gives the session up before Run, for example when the WebSocket
// upgrade failed. It does nothing after Run started.
func (se *Session) Abort() {
	l := se.l
	if !l.abandonIfIdle() {
		return
	}
	l.svc.interpret(l.svc.step(ct.OpenFailed{Session: l.id, Now: l.svc.cfg.Now()}))
	l.svc.release(l.id)
}

// Run starts the shell and bridges it to sock until the session ends. The
// socket is closed before Run returns. ctx ends the session when cancelled.
func (se *Session) Run(ctx context.Context, sock Socket) (err error) {
	l := se.l
	if !l.claim() {
		_ = sock.Close("The terminal is no longer available.")
		return ErrSessionGone
	}
	svc := l.svc
	r := &run{svc: svc, l: l, sock: sock, reason: defaultReason}
	defer svc.release(l.id)
	defer func() {
		if p := recover(); p != nil {
			svc.cfg.Log.Error("terminal session panicked", "session", l.id, "panic", fmt.Sprint(p))
			r.clientClosed()
			err = fmt.Errorf("terminal session %s failed unexpectedly", l.id)
		}
		_ = sock.Close(r.getReason())
	}()
	return r.run(ctx)
}

type run struct {
	svc  *Service
	l    *live
	sock Socket

	rmu    sync.Mutex // protects reason
	reason string

	once sync.Once
	wg   sync.WaitGroup
}

const defaultReason = "Terminal closed."

// setReason records why the session ended, unless an earlier cause is set.
func (r *run) setReason(why string) {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	if r.reason == defaultReason {
		r.reason = why
	}
}

func (r *run) getReason() string {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	return r.reason
}

// clientClosed feeds ClientClosed once, whoever notices first.
func (r *run) clientClosed() {
	r.once.Do(func() {
		r.svc.interpret(r.svc.step(ct.ClientClosed{Session: r.l.id, Now: r.svc.cfg.Now()}))
	})
}

// start runs f in a goroutine owned by the session. A panic ends the session
// instead of the process.
func (r *run) start(name string, f func()) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			if p := recover(); p != nil {
				r.svc.cfg.Log.Error("terminal goroutine panicked", "session", r.l.id, "goroutine", name, "panic", fmt.Sprint(p))
				r.setReason("The terminal stopped unexpectedly.")
				r.start("close", r.clientClosed) // may wait for the PTY; the reader is separate
			}
		}()
		f()
	}()
}

func (r *run) run(ctx context.Context) error {
	svc, l := r.svc, r.l
	life, cancel := context.WithCancel(svc.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	p, err := r.startPTY(life)
	if err != nil {
		svc.interpret(svc.step(ct.OpenFailed{Session: l.id, Now: svc.cfg.Now()}))
		r.setReason("The terminal could not start. Check that the folder still exists and try again.")
		return err
	}
	closeNow := !l.attach(p)
	svc.interpret(svc.step(ct.Opened{Session: l.id, Pid: p.Pid(), Now: svc.cfg.Now()}))

	out := make(chan []byte, svc.cfg.OutputQueue)
	readerDone := make(chan struct{})
	writerDone := make(chan struct{})
	inCtx, cancelIn := context.WithCancel(life)
	defer cancelIn()

	r.start("reader", func() {
		defer close(readerDone)
		defer close(out) // runs first: lets the writer finish
		buf := make([]byte, readChunk)
		dropping := false
		for {
			n, err := p.Read(buf)
			if n > 0 && !dropping {
				chunk := append([]byte(nil), buf[:n]...)
				select {
				case out <- chunk:
				default:
					// The browser is too slow. Keep reading so the terminal
					// never blocks (docs/spikes.md B1) but drop the client.
					dropping = true
					r.setReason("The browser could not keep up with the terminal output.")
					r.start("close", r.clientClosed)
				}
			}
			if err != nil {
				return
			}
		}
	})
	r.start("writer", func() {
		defer close(writerDone)
		failed := false
		for chunk := range out {
			if failed {
				continue
			}
			wctx, c := context.WithTimeout(life, svc.cfg.WriteTimeout)
			err := r.sock.Write(wctx, chunk)
			c()
			if err != nil {
				failed = true
				r.clientClosed()
			}
		}
	})
	r.start("input", func() { r.input(inCtx, p) })

	if closeNow {
		_ = p.Close() // a close was asked for while the shell was starting
	}

	code, werr := p.Wait()
	if werr != nil {
		code = -1
	}
	select {
	case <-readerDone:
	case <-time.After(drainWait):
		_ = p.Close()
		select {
		case <-readerDone:
		case <-time.After(closeWait):
			svc.cfg.Log.Warn("terminal output did not end", "session", l.id)
		}
	}
	_ = p.Close() // release the terminal; idempotent
	select {
	case <-writerDone:
	case <-time.After(closeWait):
	}
	l.mu.Lock()
	byUser := l.closeReq
	l.mu.Unlock()
	if !byUser {
		r.setReason(fmt.Sprintf("The shell exited with code %d.", code))
	}
	svc.interpret(svc.step(ct.ShellExited{Session: l.id, Code: code, Now: svc.cfg.Now()}))
	cancelIn()
	r.wg.Wait()
	return nil
}

func (r *run) startPTY(ctx context.Context) (platform.PTY, error) {
	svc := r.svc
	dir, err := svc.cfg.Folders.Dir(ctx, r.l.folder)
	if err != nil {
		return nil, fmt.Errorf("terminal folder: %w", err)
	}
	env, err := svc.cfg.Env.Build(safety.EnvOptions{Extra: map[string]string{"TERM": "xterm-256color"}})
	if err != nil {
		return nil, fmt.Errorf("terminal environment: %w", err)
	}
	p, err := svc.cfg.Start(ctx, platform.PTYSpec{
		Spec: platform.Spec{Argv: svc.cfg.Shell(), Dir: dir, Env: env},
		Size: platform.Size{Cols: r.l.size.Cols, Rows: r.l.size.Rows},
	})
	if err != nil {
		return nil, fmt.Errorf("start terminal shell: %w", err)
	}
	return p, nil
}

// input forwards client frames to the PTY until the socket ends.
func (r *run) input(ctx context.Context, p platform.PTY) {
	for {
		kind, data, err := r.sock.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.clientClosed()
			}
			return
		}
		f, err := ct.ParseFrame(kind, data)
		if err != nil {
			r.setReason("The browser sent something the terminal does not accept.")
			r.clientClosed()
			return
		}
		switch f := f.(type) {
		case ct.DataFrame:
			_, _ = p.Write(f.Data) // an error means the shell is ending
		case ct.ResizeFrame:
			r.svc.interpret(r.svc.step(ct.Resized{Session: r.l.id, Size: f.Size}))
		}
	}
}
