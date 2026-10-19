package repos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// ActiveSource lists the tracked (active) projects and repos. *db.Store
// implements it: its reads return active rows only (SF-02).
type ActiveSource interface {
	ListProjects(ctx context.Context) ([]core.Project, error)
	ListRepos(ctx context.Context, project core.ProjectID) ([]core.Repo, error)
}

// Publisher tells subscribers that status changed. The service calls it from
// its own goroutine, never while it holds the state, so an implementation may
// call back into the service (Snapshot).
type Publisher interface {
	// RepoChanged: one repo's view changed (or the repo is gone).
	RepoChanged(id core.RepoID)
	// CountsChanged: the repo list or the counts changed.
	CountsChanged()
}

// StatusOption changes a default of NewStatusService.
type StatusOption func(*StatusService)

// WithMaxReads sets the number of concurrent reads (default corerepos.DefaultMax).
func WithMaxReads(n int) StatusOption { return func(s *StatusService) { s.max = n } }

// WithReadTimeout sets the limit of one read (default corerepos.ReadTimeout).
func WithReadTimeout(d time.Duration) StatusOption { return func(s *StatusService) { s.timeout = d } }

// WithTickEvery sets the interval of the refresh ticker (default corerepos.TickEvery).
func WithTickEvery(d time.Duration) StatusOption { return func(s *StatusService) { s.tickEvery = d } }

// WithLogger sets the logger (default slog.Default()).
func WithLogger(l *slog.Logger) StatusOption { return func(s *StatusService) { s.log = l } }

// StatusService keeps the in-memory status of every tracked repo (WS-02 to
// WS-04). One goroutine owns the corerepos.Refresher; every input, from the
// ticker, the API, tracking and the read workers, reaches it through a
// channel. It interprets the commands Refresh returns.
//
// Wiring (cmd/ygg):
//
//	svc := repos.NewStatusService(store, repos.NewGitReader(g), clock, nil)
//	g.Go(func() error { return svc.Start(ctx) })
//	svc.SetPublisher(p)
//
// It implements api.StatusService.
type StatusService struct {
	src       ActiveSource
	reader    StatusReader
	clock     func() time.Time
	max       int
	timeout   time.Duration
	tickEvery time.Duration
	log       *slog.Logger

	pub atomic.Pointer[Publisher]
	// sem bounds the reads that run at once (belt and braces: the core counts
	// orphaned reads too).
	sem *semaphore.Weighted

	in      chan message
	pubs    chan corerepos.RefreshCommand
	running atomic.Bool
	done    chan struct{}
	doneMu  sync.Once
}

// message is what the owner goroutine receives.
type message interface{ isMessage() }

type inputMsg struct{ in corerepos.RefreshInput }

type snapshotMsg struct{ reply chan corerepos.Refresher }

// reloadMsg asks the owner to load the active set and apply it. The owner
// answers every reload it has queued with one load, so a reload can never
// apply an older set after a newer one.
type reloadMsg struct{ reply chan error }

func (inputMsg) isMessage()    {}
func (snapshotMsg) isMessage() {}
func (reloadMsg) isMessage()   {}

// NewStatusService returns a service. pub may be nil and set later with
// SetPublisher. clock returns the current time (UTC).
func NewStatusService(src ActiveSource, reader StatusReader, clock func() time.Time, pub Publisher, opts ...StatusOption) *StatusService {
	s := &StatusService{
		src: src, reader: reader, clock: clock,
		max: corerepos.DefaultMax, timeout: corerepos.ReadTimeout, tickEvery: corerepos.TickEvery,
		log:  slog.Default(),
		in:   make(chan message, 64),
		pubs: make(chan corerepos.RefreshCommand, 256),
		done: make(chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	if s.max < 1 {
		s.max = corerepos.DefaultMax
	}
	s.sem = semaphore.NewWeighted(int64(s.max))
	if pub != nil {
		s.pub.Store(&pub)
	}
	return s
}

// SetPublisher sets where changes are published. It is safe at any time.
func (s *StatusService) SetPublisher(p Publisher) {
	if p == nil {
		s.pub.Store(nil)
		return
	}
	s.pub.Store(&p)
}

var errNotRunning = apperr.New(apperr.Unavailable, "Repo status is not running. Start Yggdrasil again, then retry.", errors.New("status service is not running"))

// Start loads the active repos, queues them all, and runs until ctx is
// cancelled. Run it in the application's errgroup. It returns nil on a clean
// stop. All goroutines it starts (owner, ticker, publisher, read workers) end
// before it returns.
func (s *StatusService) Start(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("start repo status: already started")
	}
	defer s.doneMu.Do(func() { close(s.done) })
	active, err := s.loadActive(ctx)
	if err != nil {
		return fmt.Errorf("start repo status: %w", err)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { s.publishLoop(gctx); return nil })
	g.Go(func() error { s.tickLoop(gctx); return nil })
	g.Go(func() error { s.own(gctx, g, active); return nil })
	return g.Wait()
}

// loadActive reads the active set and parses it into core types. A repo whose
// stored path has a bad shape is logged and left out.
func (s *StatusService) loadActive(ctx context.Context) ([]corerepos.RepoRef, error) {
	projects, err := s.src.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var out []corerepos.RepoRef
	for _, p := range projects {
		rs, err := s.src.ListRepos(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("list repos of project %s: %w", p.ID, err)
		}
		for _, r := range rs {
			path, err := corerepos.NewCleanPath(r.Path)
			if err != nil {
				s.log.Warn("repo status skips a repo with an unusable path", "repo", r.ID, "err", err)
				continue
			}
			out = append(out, corerepos.RepoRef{ID: r.ID, Path: path})
		}
	}
	return out, nil
}

// own is the owner goroutine: the only code that touches the state.
func (s *StatusService) own(ctx context.Context, g *errgroup.Group, active []corerepos.RepoRef) {
	state := corerepos.NewRefresher(s.max)
	state = s.step(ctx, g, state, corerepos.ReposChanged{Active: active})
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.in:
			switch x := m.(type) {
			case inputMsg:
				state = s.step(ctx, g, state, x.in)
			case snapshotMsg:
				x.reply <- copyState(state)
			case reloadMsg:
				state = s.reload(ctx, g, state, x)
			}
		}
	}
}

// reload answers a reload and every other reload already queued with one load
// of the active set, applying the queued inputs in order first.
func (s *StatusService) reload(ctx context.Context, g *errgroup.Group, state corerepos.Refresher, first reloadMsg) corerepos.Refresher {
	replies := []chan error{first.reply}
drain:
	for {
		select {
		case m := <-s.in:
			switch y := m.(type) {
			case reloadMsg:
				replies = append(replies, y.reply)
			case inputMsg:
				state = s.step(ctx, g, state, y.in)
			case snapshotMsg:
				y.reply <- copyState(state)
			}
		default:
			break drain
		}
	}
	active, err := s.loadActive(ctx)
	if err == nil {
		state = s.step(ctx, g, state, corerepos.ReposChanged{Active: active})
	} else {
		err = fmt.Errorf("reload repos for status: %w", err)
	}
	for _, r := range replies {
		r <- err
	}
	return state
}

// step runs the core and interprets its commands. A panic in the core is a
// broken invariant: log it and rebuild the state from the active set.
func (s *StatusService) step(ctx context.Context, g *errgroup.Group, state corerepos.Refresher, in corerepos.RefreshInput) (next corerepos.Refresher) {
	var cmds []corerepos.RefreshCommand
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("repo status core panicked; restarting its state", "panic", fmt.Sprint(r))
				next, cmds = s.restart(ctx, state)
			}
		}()
		next, cmds = refreshCore(state, in)
	}()
	for _, c := range cmds {
		switch x := c.(type) {
		case corerepos.ReadStatus:
			g.Go(func() error { s.read(ctx, x); return nil })
		case corerepos.PublishStatus, corerepos.PublishCounts:
			select {
			case s.pubs <- c:
			default:
				s.log.Warn("repo status publish queue is full; clients will refetch")
			}
		}
	}
	return next
}

// refreshCore is the core step; tests replace it to simulate a panic.
var refreshCore = corerepos.Refresh

// safeRefresh runs the core step and reports false when it panicked.
func safeRefresh(state corerepos.Refresher, in corerepos.RefreshInput) (next corerepos.Refresher, cmds []corerepos.RefreshCommand, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	next, cmds = refreshCore(state, in)
	return next, cmds, true
}

// restart rebuilds the state from the active set. It keeps Seq and turns the
// reads of the old state into orphans (Restarted), so attempts stay unique and
// the bound holds while the old workers finish.
func (s *StatusService) restart(ctx context.Context, old corerepos.Refresher) (corerepos.Refresher, []corerepos.RefreshCommand) {
	active, err := s.loadActive(ctx)
	if err != nil {
		s.log.Error("repo status could not reload the repos", "err", err)
	}
	// A second panic must not take the process down: fall back to the empty
	// restarted state, which the next reload fills.
	next, cmds, ok := safeRefresh(old.Restarted(), corerepos.ReposChanged{Active: active})
	if !ok {
		s.log.Error("repo status core panicked again while restarting; starting from an empty state")
		return old.Restarted(), nil
	}
	return next, cmds
}

// read is one worker: it reads a repo within the read timeout and sends the
// result back to the owner. It never blocks the owner.
func (s *StatusService) read(ctx context.Context, c corerepos.ReadStatus) {
	if err := s.sem.Acquire(ctx, 1); err != nil {
		return // shutting down
	}
	rctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	snap, kind, err := s.reader.Read(rctx, c.Path)
	s.sem.Release(1)
	if ctx.Err() != nil {
		return // shutting down: the result is dropped
	}
	now := s.clock()
	var in corerepos.RefreshInput
	if err == nil {
		in = corerepos.StatusRead{ID: c.ID, Attempt: c.Attempt, Snap: snap, Now: now}
	} else {
		if kind.Validate() != nil {
			kind = corerepos.ProblemGitFailed
		}
		s.log.Info("repo status read failed", "repo", c.ID, "kind", string(kind), "err", err)
		in = corerepos.StatusFailed{ID: c.ID, Attempt: c.Attempt, Kind: kind, Now: now}
	}
	select {
	case s.in <- inputMsg{in: in}:
	case <-ctx.Done():
	}
}

func (s *StatusService) tickLoop(ctx context.Context) {
	t := time.NewTicker(s.tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			select {
			case s.in <- inputMsg{in: corerepos.Tick{Now: s.clock()}}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *StatusService) publishLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.pubs:
			p := s.pub.Load()
			if p == nil {
				continue
			}
			switch x := c.(type) {
			case corerepos.PublishStatus:
				(*p).RepoChanged(x.ID)
			case corerepos.PublishCounts:
				(*p).CountsChanged()
			}
		}
	}
}

// send hands a message to the owner; it fails when the service is not
// running or ctx ends first.
func (s *StatusService) send(ctx context.Context, m message) error {
	if !s.running.Load() {
		return errNotRunning
	}
	select {
	case s.in <- m:
		return nil
	case <-s.done:
		return errNotRunning
	case <-ctx.Done():
		return fmt.Errorf("send to repo status: %w", ctx.Err())
	}
}

// Refresh asks for a new read of the repos; empty ids means all.
func (s *StatusService) Refresh(ctx context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error {
	if err := reason.Validate(); err != nil {
		return apperr.New(apperr.Invalid, "The refresh reason is not valid.", err)
	}
	return s.send(ctx, inputMsg{in: corerepos.RefreshRequested{IDs: ids, Reason: reason, Now: s.clock()}})
}

// ReloadRepos re-reads the active set from the store and tells the status
// about it. Repo tracking calls it after a commit. New repos are read at once.
func (s *StatusService) ReloadRepos(ctx context.Context) error {
	reply := make(chan error, 1)
	if err := s.send(ctx, reloadMsg{reply: reply}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-s.done:
		return errNotRunning
	case <-ctx.Done():
		return fmt.Errorf("reload repos for status: %w", ctx.Err())
	}
}

// Snapshot returns a copy of the state: every entry, and which reads are in
// flight. It is taken by the owner goroutine, so it is consistent.
func (s *StatusService) Snapshot(ctx context.Context) (corerepos.Refresher, error) {
	reply := make(chan corerepos.Refresher, 1)
	if err := s.send(ctx, snapshotMsg{reply: reply}); err != nil {
		return corerepos.Refresher{}, err
	}
	select {
	case r := <-reply:
		return r, nil
	case <-s.done:
		return corerepos.Refresher{}, errNotRunning
	case <-ctx.Done():
		return corerepos.Refresher{}, fmt.Errorf("read repo status: %w", ctx.Err())
	}
}

func copyState(r corerepos.Refresher) corerepos.Refresher {
	out := corerepos.NewRefresher(r.Max)
	out.Seq = r.Seq
	for k, v := range r.Orphans {
		out.Orphans[k] = v
	}
	for k, v := range r.Entries {
		out.Entries[k] = v
	}
	for k, v := range r.InFlight {
		out.InFlight[k] = v
	}
	out.Queue = append(out.Queue, r.Queue...)
	return out
}
