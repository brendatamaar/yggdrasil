// Package devservers is the shell of dev servers (DV-01 to DV-03, DV-05;
// docs/design/devservers.md). The decisions are pure and live in
// internal/core/devservers (Step). This package parses outside data into
// core types, loads the state, calls Step, persists the new state and its
// audit entries in one transaction, and only then runs the commands: it
// starts processes through platform.Start with the clean environment in a
// tracked folder, polls ports, schedules wakes, keeps the logs and tells the
// UI.
package devservers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

const (
	systemActor   = "system"
	userActor     = "user"
	commitTries   = 3
	feedTimeout   = 10 * time.Second
	announceDelay = 250 * time.Millisecond
	defaultDrain  = 5 * time.Second
	logSubfolder  = "devservers"
	logParent     = "logs"
)

// View is a server as the API shows it. Warnings are derived (DV-02).
type View struct {
	ID         string
	Key        dsc.Key
	Status     string
	Attempt    int
	Pid        int
	Block      core.PortBlock
	Ports      []dsc.Port
	Announced  []dsc.Port
	Warnings   []dsc.Port
	Since      time.Time
	Last       dsc.LastKind
	Fail       dsc.FailKind
	ExitCode   int
	StopCause  dsc.StopCause
	Message    string
	Restarting bool
}

// run is the live attempt of a server: its process and what its output said.
type run struct {
	attempt int
	cancel  context.CancelFunc

	mu        sync.Mutex
	pid       int
	announced map[dsc.Port]struct{}
	pending   bool
}

// Service runs dev servers.
type Service struct {
	d     Deps
	log   *slog.Logger
	clock Clock
	wakes *Wakes

	base   context.Context
	cancel context.CancelFunc
	g      *errgroup.Group

	mu       sync.Mutex // serialises apply and guards the maps below
	closed   bool       // no new start; shutdown began
	stopped  bool       // no new goroutine
	runs     map[string]*run
	sinks    map[string]LogSink
	restarts map[string]dsc.Key
	msgs     map[string]string
	drained  chan struct{}
}

// New returns a service whose goroutines end when ctx ends or Close is called.
func New(ctx context.Context, d Deps) (*Service, error) {
	if d.Store == nil || d.Repos == nil || d.Trust == nil || d.Templates == nil || d.Blocks == nil ||
		d.Start == nil || d.KillTask == nil || d.Resolve == nil || d.Ports == nil || d.Env == nil || d.Redactor == nil ||
		d.Orphans.Record == nil || d.Orphans.Forget == nil || d.Orphans.List == nil || d.Orphans.Stop == nil {
		return nil, errors.New("devservers: missing dependency")
	}
	if d.Clock == nil {
		d.Clock = RealClock{}
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.DrainTimeout <= 0 {
		d.DrainTimeout = defaultDrain
	}
	base, cancel := context.WithCancel(ctx)
	g, _ := errgroup.WithContext(base)
	return &Service{
		d: d, log: d.Log, clock: d.Clock, wakes: NewWakes(d.Clock), base: base, cancel: cancel, g: g,
		runs: map[string]*run{}, sinks: map[string]LogSink{}, restarts: map[string]dsc.Key{}, msgs: map[string]string{},
		drained: make(chan struct{}),
	}, nil
}

// Start starts the server for command in the target. The target must be a
// tracked repo or one of its active worktrees, and a trusted one (SF-09).
func (s *Service) Start(ctx context.Context, t Target, command string) (View, error) {
	key, err := ParseStart(string(t.Repo), string(t.Worktree), command)
	if err != nil {
		return View{}, err
	}
	if s.isClosed() {
		return View{}, closingError()
	}
	dir, err := s.d.Repos.Dir(ctx, t)
	if err != nil {
		return View{}, err
	}
	return s.startIn(ctx, t, key, dir)
}

func closingError() error {
	return apperr.New(apperr.Unavailable, "Yggdrasil is closing. Start the server again after it reopens.", nil)
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Service) startIn(ctx context.Context, t Target, key dsc.Key, dir string) (View, error) {
	if err := s.trustGate(ctx, t); err != nil {
		return View{}, err
	}
	argv, env, err := s.d.Templates.DevServer(ctx, t, key.CommandKey)
	if err != nil {
		return View{}, err
	}
	tpl, err := dsc.ParseTemplate(argv, env)
	if err != nil {
		return View{}, apperr.New(apperr.Invalid, "This server's command cannot be started: "+plainTemplateMessage(err.Error()), err)
	}
	var block core.PortBlock
	if t.Worktree != "" {
		if block, err = s.d.Blocks.BlockFor(ctx, t); err != nil {
			return View{}, err
		}
	}
	mk := func(cur dsc.Server) dsc.Input {
		return dsc.StartRequested{Attempt: dsc.AttemptOf(cur) + 1, Now: s.clock.Now(), Template: tpl, Block: block}
	}
	next, err := s.apply(ctx, key, userActor, mk, dir)
	return s.view(key, next), err
}

// trustGate refuses a repo that ships agent hooks or MCP servers and has not
// been trusted (SF-09). An error reading the trust view refuses too.
func (s *Service) trustGate(ctx context.Context, t Target) error {
	v, err := s.d.Trust.TrustView(ctx, t.Repo)
	if err != nil {
		return apperr.New(apperr.Unavailable, "Yggdrasil could not check whether this repo is trusted. Try again.", err)
	}
	if !v.Admissible() {
		return apperr.New(apperr.Conflict, "This repo ships its own agent hooks or MCP servers. Review and trust it before starting its dev server.", nil)
	}
	return nil
}

// Stop stops the server (a no-op if it is at rest).
func (s *Service) Stop(ctx context.Context, t Target, command string) (View, error) {
	key, err := ParseStart(string(t.Repo), string(t.Worktree), command)
	if err != nil {
		return View{}, err
	}
	next, err := s.apply(ctx, key, userActor, func(dsc.Server) dsc.Input {
		return dsc.StopRequested{Cause: dsc.CauseUserStop, Now: s.clock.Now()}
	}, "")
	return s.view(key, next), err
}

// Restart stops the server and starts it again when it has stopped. A server
// at rest just starts. The core never queues the second start: this method
// does.
func (s *Service) Restart(ctx context.Context, t Target, command string) (View, error) {
	key, err := ParseStart(string(t.Repo), string(t.Worktree), command)
	if err != nil {
		return View{}, err
	}
	if s.isClosed() {
		return View{}, closingError()
	}
	cur, _, err := s.current(ctx, key)
	if err != nil {
		return View{}, err
	}
	switch cur.(type) {
	case dsc.Stopped, dsc.Failed:
		return s.Start(ctx, t, command)
	case dsc.Orphaned:
		return View{}, apperr.New(apperr.Conflict, "This server was left over from a crashed run. Stop it first, then start it.", nil)
	}
	s.mu.Lock()
	s.restarts[IDOf(key)] = key
	s.mu.Unlock()
	next, err := s.apply(ctx, key, userActor, func(dsc.Server) dsc.Input {
		return dsc.StopRequested{Cause: dsc.CauseRestart, Now: s.clock.Now()}
	}, "")
	if err != nil {
		s.mu.Lock()
		delete(s.restarts, IDOf(key))
		s.mu.Unlock()
	}
	v := s.view(key, next)
	v.Restarting = true
	return v, err
}

// Get returns one server (stopped, never started, if it has no row).
func (s *Service) Get(ctx context.Context, t Target, command string) (View, error) {
	key, err := ParseStart(string(t.Repo), string(t.Worktree), command)
	if err != nil {
		return View{}, err
	}
	cur, _, err := s.current(ctx, key)
	if err != nil {
		return View{}, err
	}
	return s.view(key, cur), nil
}

// List returns the servers stored for a repo (its main checkout and worktrees).
func (s *Service) List(ctx context.Context, repo core.RepoID) ([]View, error) {
	rows, err := s.d.Store.ListByRepo(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("list dev servers: %w", err)
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		st, err := dsc.ServerFromRow(r.Row)
		if err != nil {
			s.log.Warn("skipping an unreadable dev server row", "id", r.ID, "err", err)
			continue
		}
		out = append(out, s.view(r.Row.Key, st))
	}
	return out, nil
}

// Logs returns the last n log lines of the server (all the ring holds if n<0).
// They are already redacted.
func (s *Service) Logs(t Target, command string, n int) ([]string, error) {
	key, err := ParseStart(string(t.Repo), string(t.Worktree), command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	sink := s.sinks[IDOf(key)]
	s.mu.Unlock()
	if sink == nil {
		return []string{}, nil
	}
	return sink.Tail(n), nil
}

func (s *Service) current(ctx context.Context, key dsc.Key) (dsc.Server, int64, error) {
	return loadServer(ctx, s.d.Store.Get, IDOf(key))
}

func loadServer(ctx context.Context, get func(context.Context, string) (db.DevServerRow, error), id string) (dsc.Server, int64, error) {
	row, err := get(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return dsc.Stopped{Last: dsc.LastNever}, db.NewDevServer, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load dev server %s: %w", id, err)
	}
	st, err := dsc.ServerFromRow(row.Row)
	if err != nil {
		return nil, 0, fmt.Errorf("load dev server %s: %w", id, err)
	}
	return st, row.Version, nil
}

func (s *Service) view(key dsc.Key, st dsc.Server) View {
	if st == nil {
		st = dsc.Stopped{Last: dsc.LastNever}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked(key, st)
}

// viewLocked is view for callers that hold s.mu.
func (s *Service) viewLocked(key dsc.Key, st dsc.Server) View {
	if st == nil {
		st = dsc.Stopped{Last: dsc.LastNever}
	}
	id := IDOf(key)
	msg := s.msgs[id]
	_, restarting := s.restarts[id]
	v := View{ID: id, Key: key, Status: st.Status(), Attempt: dsc.AttemptOf(st), Message: msg, Restarting: restarting}
	switch x := st.(type) {
	case dsc.Stopped:
		v.Since, v.Last, v.ExitCode = x.At, x.Last, x.Code
	case dsc.Starting:
		v.Since, v.Pid, v.Block = x.Since, x.Pid, x.Block
	case dsc.Running:
		v.Since, v.Pid, v.Block, v.Ports, v.Announced, v.Warnings = x.Since, x.Pid, x.Block, x.Ports, x.Announced, x.Warnings()
	case dsc.Stopping:
		v.Since, v.Pid, v.Block, v.StopCause = x.Since, x.Pid, x.Block, x.Cause
	case dsc.Failed:
		v.Since, v.Fail, v.ExitCode = x.At, x.Kind, x.Code
		if x.Message != "" {
			v.Message = x.Message
		}
	case dsc.Orphaned:
		v.Since, v.Pid = x.SeenAt, x.Pid
	}
	return v
}

// apply is the one path of every change: load, Step, persist state and audit
// together, then run the commands. mk builds the input from the loaded state
// (nil means nothing to do). dir is the tracked folder for a StartProcess.
// Calls are serialised; the commands never call apply while the lock is held
// (they start goroutines).
func (s *Service) apply(ctx context.Context, key dsc.Key, actor string, mk func(dsc.Server) dsc.Input, dir string) (dsc.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := IDOf(key)
	var lastErr error
	for range commitTries {
		cur, version, err := loadServer(ctx, s.d.Store.Get, id)
		if err != nil {
			return nil, err
		}
		in := mk(cur)
		if in == nil {
			return cur, nil
		}
		if _, isStart := in.(dsc.StartRequested); isStart && s.closed {
			return cur, closingError()
		}
		next, cmds := dsc.Step(cur, in)
		changed := !reflect.DeepEqual(dsc.Persisted(cur), dsc.Persisted(next))
		now := s.clock.Now()
		audits, err := s.prepareAudits(key, id, actor, cmds, now)
		if err != nil {
			return cur, err
		}
		if changed || len(audits) > 0 {
			err = s.d.Store.Commit(ctx, func(tx Tx) error {
				if changed {
					if _, err := tx.Save(ctx, id, dsc.RowOf(key, next), version, now); err != nil {
						return err
					}
				}
				for _, a := range audits {
					if err := tx.Audit(ctx, a, core.AuditID(s.d.IDs("aud")), now); err != nil {
						return err
					}
				}
				return nil
			})
			if errors.Is(err, db.ErrConflict) {
				lastErr = err
				continue
			}
			if err != nil {
				return cur, apperr.New(apperr.Unavailable, "Yggdrasil could not save the server's state. Try again.", fmt.Errorf("commit dev server %s: %w", id, err))
			}
		}
		return next, s.afterCommit(key, id, next, cmds, dir)
	}
	return nil, apperr.New(apperr.Conflict, "The server changed while Yggdrasil was updating it. Try again.", lastErr)
}

func (s *Service) prepareAudits(key dsc.Key, id, actor string, cmds []dsc.Command, now time.Time) ([]core.AuditEntry, error) {
	var out []core.AuditEntry
	for _, c := range cmds {
		w, ok := c.(dsc.WriteAudit)
		if !ok {
			continue
		}
		detail := map[string]string{"repo": string(key.Repo), "command": key.CommandKey}
		if key.Worktree != "" {
			detail["worktree"] = string(key.Worktree)
		}
		for k, v := range w.Detail {
			detail[k] = v
		}
		raw, err := json.Marshal(detail)
		if err != nil {
			return nil, fmt.Errorf("audit detail: %w", err)
		}
		out = append(out, core.AuditEntry{Actor: actor, Action: string(w.Action), Subject: "devserver:" + id, Detail: raw})
	}
	return out, nil
}

// feed applies an input that came from a process, a timer or a poll.
func (s *Service) feed(key dsc.Key, in dsc.Input) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), feedTimeout)
	defer cancel()
	if _, err := s.apply(ctx, key, systemActor, func(dsc.Server) dsc.Input { return in }, ""); err != nil {
		s.log.Warn("could not apply a dev server event", "id", IDOf(key), "event", fmt.Sprintf("%T", in), "err", err)
	}
}

// spawn runs f in the service group. It reports false after shutdown. A panic
// is recovered and logged; f itself feeds the exit of its attempt.
func (s *Service) spawn(f func()) bool {
	if s.stopped {
		return false
	}
	s.g.Go(func() error {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("dev server goroutine panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			}
		}()
		f()
		return nil
	})
	return true
}

func wakeKey(id string, attempt int, kind dsc.WakeKind) string {
	return fmt.Sprintf("%s|%d|%s", id, attempt, kind)
}

// afterCommit runs the commands of a step (called with s.mu held, after the
// state and its audit were committed).
func (s *Service) afterCommit(key dsc.Key, id string, next dsc.Server, cmds []dsc.Command, dir string) error {
	if f, ok := next.(dsc.Failed); ok && f.Message != "" {
		s.msgs[id] = f.Message
	} else if _, ok := next.(dsc.Starting); ok {
		delete(s.msgs, id)
	}
	var rejected error
	for _, c := range cmds {
		switch c := c.(type) {
		case dsc.StartProcess:
			s.startRun(key, id, dir, c)
		case dsc.KillProcess:
			s.kill(id, c.Attempt)
		case dsc.PollPorts:
			s.poll(key, id, c.Attempt)
		case dsc.ScheduleWake:
			s.wakes.At(wakeKey(id, c.Attempt, c.Kind), c.At, func(now time.Time) {
				s.feed(key, dsc.Wake{Attempt: c.Attempt, Kind: c.Kind, Now: now})
			})
		case dsc.StopOrphan:
			s.stopOrphan(key, id, c.Pid)
		case dsc.Publish:
			if s.d.Hub != nil {
				s.d.Hub.PublishServer(id, s.viewLocked(key, next))
			}
		case dsc.Reject:
			rejected = rejectError(c)
		}
	}
	s.afterState(key, id, next)
	return rejected
}

// afterState finishes what a state implies: a pending restart starts when the
// server is at rest, and announcements that arrived while it was starting are
// sent once it runs.
func (s *Service) afterState(key dsc.Key, id string, next dsc.Server) {
	switch st := next.(type) {
	case dsc.Stopped, dsc.Failed:
		if rk, ok := s.restarts[id]; ok {
			delete(s.restarts, id)
			t := Target{Repo: rk.Repo, Worktree: rk.Worktree}
			s.spawn(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), feedTimeout)
				defer cancel()
				if _, err := s.Start(ctx, t, rk.CommandKey); err != nil {
					s.log.Warn("restart could not start the server", "id", id, "err", err)
				}
			})
		}
	case dsc.Running:
		if r := s.runs[id]; r != nil && r.attempt == st.Attempt && !r.sameAnnounced(st.Announced) {
			s.spawn(func() { s.sendAnnounced(key, r) })
		}
	}
}

// Close stops every server and waits for the processes to end, at most
// DrainTimeout, then stops the service's goroutines. Call it before the store
// closes: the exits are committed here. A process that outlives the wait ends
// with the job object when Yggdrasil exits; its row is read as interrupted at
// the next start.
func (s *Service) Close(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	rows, err := s.d.Store.ListLive(ctx)
	if err != nil {
		s.log.Warn("could not list dev servers at shutdown", "err", err)
	}
	for _, r := range rows {
		key := r.Row.Key
		now := s.clock.Now()
		if _, err := s.apply(ctx, key, systemActor, func(dsc.Server) dsc.Input { return dsc.ShutdownRequested{Now: now} }, ""); err != nil {
			s.log.Warn("could not stop a dev server at shutdown", "id", r.ID, "err", err)
		}
	}
	s.signalIfDrained()
	timeout := make(chan struct{})
	t := s.clock.AfterFunc(s.d.DrainTimeout, func() { close(timeout) })
	select {
	case <-s.drained:
	case <-timeout:
		s.log.Warn("dev servers did not stop within the shutdown wait; the job object ends them")
	case <-ctx.Done():
	}
	t.Stop()

	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.wakes.Close()
	s.cancel()
	_ = s.g.Wait()
	s.mu.Lock()
	for _, sink := range s.sinks {
		_ = sink.Close()
	}
	s.mu.Unlock()
}

// signalIfDrained closes drained when shutdown began and no process runs.
func (s *Service) signalIfDrained() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signalLocked()
}

func (s *Service) signalLocked() {
	if s.closed && len(s.runs) == 0 {
		select {
		case <-s.drained:
		default:
			close(s.drained)
		}
	}
}

func (s *Service) logDir() string {
	return filepath.Join(s.d.DataDir, logParent, logSubfolder)
}
