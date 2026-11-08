package terminal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// DefaultSize is the terminal size until the browser sends its own.
var DefaultSize = ct.Size{Cols: 80, Rows: 24}

// Config wires a Service. Folders, Env and Audit are required; the rest have
// production defaults.
type Config struct {
	Folders Folders
	// Env builds the clean environment of the shell (docs/AGENTS.md 5.3).
	Env   safety.EnvBuilder
	Audit AuditSink

	Tokens TokenSource                             // default: crypto/rand
	Start  PTYStarter                              // default: platform.StartPTY
	Shell  func() []string                         // default: platform.DefaultShell
	Now    func() time.Time                        // default: time.Now
	After  func(time.Duration, func()) func() bool // default: time.AfterFunc
	NewID  func() core.TerminalSessionID           // default: random "term_<hex>"

	// OutputQueue is how many output chunks may wait for a slow browser
	// before it is disconnected. Default 64 (of up to 4 KiB each).
	OutputQueue int
	// WriteTimeout bounds one socket write. Default 10 s.
	WriteTimeout time.Duration
	// AuditBackoff is the first wait before a failed close audit is retried;
	// it doubles on each of 3 retries. Default 50 ms.
	AuditBackoff time.Duration

	Log *slog.Logger
}

// Service is the terminal service. Create it with New and call Close on
// shutdown. All methods are safe for concurrent use.
//
// # API for the HTTP and WebSocket handlers (internal/api)
//
// Issue (POST /api/terminal): call ct.ParseIssue on the body, then
// Service.Issue(ctx, target). It returns the raw token and its lifetime in
// seconds; this is the only place the raw token exists. Host and Origin are
// checked by the middleware before this.
//
// Redeem (GET /ws/terminal?token=...): the handler MUST check Host and Origin
// first and reject a bad one without calling the service, because a rejected
// request must not burn the token (a cross-site page would otherwise spend a
// real user's token). Then call Redeem(ctx, token, size); a *RefusedError maps
// to an HTTP error with its Message (nothing is upgraded). On success upgrade
// the connection and call Session.Run(ctx, socket), which blocks until the
// session ends. If the upgrade fails, call Session.Abort. Open does Redeem and
// Run in one call for callers that already hold a socket.
//
// CloseSession is the UI close button. Close drains everything on shutdown.
type Service struct {
	cfg Config

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex // protects everything below
	reg     ct.Registry
	live    map[core.TerminalSessionID]*live
	audits  []AuditEntry
	openErr map[core.TerminalSessionID]error // failed open audits, read by Redeem
	timers  map[timerKey]func() bool
	closed  bool

	auditMu sync.Mutex // serializes audit writes in step order
	wg      sync.WaitGroup
}

// New returns a Service.
func New(c Config) (*Service, error) {
	if c.Folders == nil || c.Env == nil || c.Audit == nil {
		return nil, errors.New("terminal: Folders, Env and Audit are required")
	}
	if c.Tokens == nil {
		c.Tokens = CryptoTokens{}
	}
	if c.Start == nil {
		c.Start = platform.StartPTY
	}
	if c.Shell == nil {
		c.Shell = platform.DefaultShell
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.After == nil {
		c.After = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if c.NewID == nil {
		c.NewID = randomSessionID
	}
	if c.OutputQueue <= 0 {
		c.OutputQueue = 64
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.AuditBackoff <= 0 {
		c.AuditBackoff = 50 * time.Millisecond
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		cfg: c, ctx: ctx, cancel: cancel,
		reg:     ct.NewRegistry(),
		live:    map[core.TerminalSessionID]*live{},
		openErr: map[core.TerminalSessionID]error{},
		timers:  map[timerKey]func() bool{},
	}, nil
}

// CryptoTokens is the real TokenSource: 32 bytes from crypto/rand as
// unpadded base64url (43 characters).
type CryptoTokens struct{}

// New implements TokenSource.
func (CryptoTokens) New() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func randomSessionID() core.TerminalSessionID {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-based ID
		// keeps the session unique enough and the registry rejects a clash.
		return core.TerminalSessionID(fmt.Sprintf("term_%x", time.Now().UnixNano()))
	}
	return core.TerminalSessionID("term_" + hex.EncodeToString(b[:]))
}

// Issue makes a single-use token for a tracked folder. The token lives 30
// seconds, in memory only.
func (s *Service) Issue(ctx context.Context, t ct.Target) (token string, expiresIn time.Duration, err error) {
	if err := t.Validate(); err != nil {
		return "", 0, fmt.Errorf("terminal request: %w", err)
	}
	folder, err := s.cfg.Folders.Resolve(ctx, t)
	if err != nil {
		return "", 0, fmt.Errorf("resolve terminal folder: %w", err)
	}
	if _, err := s.cfg.Folders.Dir(ctx, folder); err != nil {
		return "", 0, fmt.Errorf("check terminal folder: %w", err)
	}
	token, err = s.cfg.Tokens.New()
	if err != nil {
		return "", 0, fmt.Errorf("make terminal token: %w", err)
	}
	hash, err := ct.ParseToken(token)
	if err != nil {
		return "", 0, fmt.Errorf("terminal token source gave a bad token: %w", err)
	}
	var granted bool
	var refusal ct.Refusal
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", 0, &RefusedError{Reason: ct.RefusedShuttingDown}
	}
	cmds := s.stepLocked(ct.IssueRequested{Hash: hash, Folder: folder, Now: s.cfg.Now()})
	if rp, ok := findReply(cmds, ct.ReplyIssue); ok {
		granted, refusal = rp.Granted, rp.Refusal
	}
	s.mu.Unlock()
	s.interpret(cmds)
	if !granted {
		return "", 0, &RefusedError{Reason: refusal}
	}
	return token, ct.TokenTTL, nil
}

func findReply(cmds []ct.Command, kind ct.ReplyKind) (ct.Reply, bool) {
	for _, c := range cmds {
		if r, ok := c.(ct.Reply); ok && r.Kind == kind {
			return r, true
		}
	}
	return ct.Reply{}, false
}

// stepLocked runs Step under s.mu, keeps the side records in line with the
// registry and queues audit entries in step order. The caller holds s.mu and
// must call interpret with the result after releasing it.
func (s *Service) stepLocked(in ct.Input) []ct.Command {
	reg, cmds := ct.Step(s.reg, in)
	s.reg = reg
	for _, c := range cmds {
		switch c := c.(type) {
		case ct.OpenPTY:
			s.live[c.Session] = &live{svc: s, id: c.Session, folder: c.Folder, size: c.Size}
			s.wg.Add(1)
		case ct.WriteAudit:
			s.audits = append(s.audits, AuditEntry{Action: c.Action, Session: c.Session, Folder: c.Folder, Cause: c.Cause, Code: c.Code, At: s.cfg.Now()})
		}
	}
	return cmds
}

func (s *Service) step(in ct.Input) []ct.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stepLocked(in)
}

// interpret runs the commands that need the outside world. OpenPTY is done by
// Session.Run (it needs the socket); Reply is read by the caller.
func (s *Service) interpret(cmds []ct.Command) {
	for _, c := range cmds {
		switch c := c.(type) {
		case ct.ResizePTY:
			if l := s.liveFor(c.Session); l != nil {
				l.resize(c.Size)
			}
		case ct.ClosePTY:
			if l := s.liveFor(c.Session); l != nil {
				l.requestClose()
			}
		case ct.ScheduleWake:
			s.scheduleWake(c.Kind, c.At)
		}
	}
	s.flushAudit()
}

func (s *Service) liveFor(id core.TerminalSessionID) *live {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

type timerKey struct {
	kind ct.WakeKind
	at   int64
}

func (s *Service) scheduleWake(kind ct.WakeKind, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	key := timerKey{kind: kind, at: at.UnixNano()}
	if _, ok := s.timers[key]; ok {
		return
	}
	d := max(at.Sub(s.cfg.Now()), 0)
	s.timers[key] = s.cfg.After(d, func() {
		s.mu.Lock()
		delete(s.timers, key)
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		s.interpret(s.step(ct.Wake{Kind: kind, Now: s.cfg.Now()}))
	})
}

// flushAudit writes queued entries, oldest first, one writer at a time.
// A failed terminal.open is remembered for Redeem, which refuses the open
// (fail closed, invariant 8). Any other entry is retried with a growing wait
// and, if it still fails, logged with IDs only.
func (s *Service) flushAudit() {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	for {
		s.mu.Lock()
		if len(s.audits) == 0 {
			s.mu.Unlock()
			return
		}
		e := s.audits[0]
		s.audits = s.audits[1:]
		s.mu.Unlock()

		attempts := 4
		if e.Action == ct.AuditOpen {
			attempts = 1
		}
		err := s.record(e, attempts)
		if err == nil {
			continue
		}
		s.cfg.Log.Error("terminal audit write failed", "action", e.Action, "session", e.Session, "attempts", attempts, "err", err)
		if e.Action == ct.AuditOpen {
			s.mu.Lock()
			s.openErr[e.Session] = err
			s.mu.Unlock()
		}
	}
}

func (s *Service) record(e AuditEntry, attempts int) (err error) {
	wait := s.cfg.AuditBackoff
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(wait)
			wait *= 2
		}
		actx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = s.cfg.Audit.Record(actx, e)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// ErrAuditUnavailable is returned when the open could not be written to the
// audit log, so no shell was started.
var ErrAuditUnavailable = errors.New("terminal: the audit log is not available")

// Redeem spends a token and reserves a session. The caller must then call
// Session.Run or Session.Abort. Check Host and Origin before calling (see the
// Service doc).
func (s *Service) Redeem(ctx context.Context, token string, size ct.Size) (*Session, error) {
	hash, err := ct.ParseToken(token)
	if err != nil {
		return nil, &RefusedError{Reason: ct.RefusedUnknown}
	}
	s.mu.Lock()
	folder, known := s.reg.FolderOf(hash)
	s.mu.Unlock()
	check := ct.FolderUntracked
	if known {
		if _, err := s.cfg.Folders.Dir(ctx, folder); err == nil {
			check = ct.FolderTracked
		}
	}
	id := s.cfg.NewID()
	s.mu.Lock()
	cmds := s.stepLocked(ct.RedeemRequested{Hash: hash, Session: id, Size: size, Check: check, Now: s.cfg.Now()})
	l := s.live[id]
	rp, _ := findReply(cmds, ct.ReplyRedeem)
	s.mu.Unlock()
	s.interpret(cmds) // writes the open audit before any shell exists
	if !rp.Granted || l == nil {
		return nil, &RefusedError{Reason: rp.Refusal}
	}
	se := &Session{l: l}
	s.mu.Lock()
	auditErr := s.openErr[id]
	delete(s.openErr, id)
	s.mu.Unlock()
	if auditErr != nil {
		se.Abort() // open_failed; the slot is free again
		return nil, ErrAuditUnavailable
	}
	return se, nil
}

// Open redeems the token and runs the session on sock until it ends. A
// refusal closes the socket with the plain-language message.
func (s *Service) Open(ctx context.Context, token string, sock Socket) error {
	se, err := s.Redeem(ctx, token, DefaultSize)
	if err != nil {
		var re *RefusedError
		if errors.As(err, &re) {
			_ = sock.Close(re.Message())
		} else if errors.Is(err, ErrAuditUnavailable) {
			_ = sock.Close("The terminal was not opened because Yggdrasil could not record it in the audit log.")
		} else {
			_ = sock.Close("The terminal could not be opened.")
		}
		return err
	}
	return se.Run(ctx, sock)
}

// CloseSession is the UI close button for one session.
func (s *Service) CloseSession(id core.TerminalSessionID) {
	s.interpret(s.step(ct.CloseRequested{Session: id, Now: s.cfg.Now()}))
}

// Close shuts the service down: no new tokens or sessions, every session
// closed, then it waits for the sessions to finish or for ctx. After ctx it
// cancels what is left. Close is safe to call more than once.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, stop := range s.timers {
		stop()
	}
	s.timers = map[timerKey]func() bool{}
	cmds := s.stepLocked(ct.Shutdown{Now: s.cfg.Now()})
	s.mu.Unlock()
	s.interpret(cmds)

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }() // ends when every session has ended
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel() // kills the remaining shells through their contexts
		<-done
		return fmt.Errorf("terminal sessions did not close in time: %w", ctx.Err())
	}
}
