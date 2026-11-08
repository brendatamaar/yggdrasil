// Package devserverstest has the fakes and the contract suites of
// internal/devservers: an in-memory Store, a Clock whose time moves only when
// told, and fakes for the capabilities the service takes (repos, trust,
// templates, port blocks, hub). The suites run against the fake and the real
// implementation (docs/AGENTS.md 5.11).
package devserverstest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// Store is an in-memory devservers.Store. Like the table it refuses rows the
// core would not parse, rows of an unknown repo, and a second insert of an ID.
type Store struct {
	mu     sync.Mutex
	rows   map[string]db.DevServerRow
	audit  []core.AuditEntry
	repos  map[core.RepoID]bool
	commit int
	// FailCommit, if set, is returned by the next Commit before fn runs.
	FailCommit error
}

// NewStore returns an empty store that knows the given repos.
func NewStore(repos ...core.RepoID) *Store {
	s := &Store{rows: map[string]db.DevServerRow{}, repos: map[core.RepoID]bool{}}
	for _, r := range repos {
		s.repos[r] = true
	}
	return s
}

// Audit returns the audit entries for subject ("" for all), oldest first.
func (s *Store) Audit(subject string) []core.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.AuditEntry
	for _, e := range s.audit {
		if subject == "" || e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// Commits is the number of successful commits.
func (s *Store) Commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit
}

type fakeTx struct {
	s     *Store
	rows  map[string]db.DevServerRow
	audit []core.AuditEntry
}

// Commit implements devservers.Store.
func (s *Store) Commit(ctx context.Context, fn func(devservers.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailCommit != nil {
		err := s.FailCommit
		s.FailCommit = nil
		return err
	}
	tx := &fakeTx{s: s, rows: make(map[string]db.DevServerRow, len(s.rows)), audit: slices.Clone(s.audit)}
	for k, v := range s.rows {
		tx.rows[k] = v
	}
	if err := fn(tx); err != nil {
		return err
	}
	s.rows, s.audit = tx.rows, tx.audit
	s.commit++
	return nil
}

func (t *fakeTx) Load(_ context.Context, id string) (db.DevServerRow, error) {
	r, ok := t.rows[id]
	if !ok {
		return db.DevServerRow{}, fmt.Errorf("load dev server %s: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (t *fakeTx) Save(_ context.Context, id string, row dsc.Row, expected int64, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("dev server needs a time from the caller")
	}
	if _, err := dsc.ServerFromRow(row); err != nil {
		return 0, fmt.Errorf("save dev server %s: %w", id, err)
	}
	cur, exists := t.rows[id]
	if expected == db.NewDevServer {
		if exists {
			return 0, fmt.Errorf("save dev server %s: %w", id, db.ErrConflict)
		}
		if !t.s.repos[row.Key.Repo] {
			return 0, fmt.Errorf("save dev server %s: %w", id, db.ErrNotFound)
		}
		t.rows[id] = db.DevServerRow{ID: id, Row: row, Version: 0}
		return 0, nil
	}
	if !exists {
		return 0, fmt.Errorf("save dev server %s: %w", id, db.ErrNotFound)
	}
	if cur.Version != expected {
		return 0, fmt.Errorf("save dev server %s: %w", id, db.ErrConflict)
	}
	row.Key = cur.Row.Key
	t.rows[id] = db.DevServerRow{ID: id, Row: row, Version: expected + 1}
	return expected + 1, nil
}

func (t *fakeTx) Audit(_ context.Context, e core.AuditEntry, id core.AuditID, at time.Time) error {
	e.ID, e.At = id, at.UTC()
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.Actor == "" || e.Action == "" || e.Subject == "" {
		return errors.New("audit entry needs actor, action and subject")
	}
	t.audit = append(t.audit, e)
	return nil
}

// Get implements devservers.Store.
func (s *Store) Get(_ context.Context, id string) (db.DevServerRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return db.DevServerRow{}, fmt.Errorf("get dev server %s: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (s *Store) list(keep func(db.DevServerRow) bool) []db.DevServerRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []db.DevServerRow{}
	for _, r := range s.rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListLive implements devservers.Store.
func (s *Store) ListLive(context.Context) ([]db.DevServerRow, error) {
	return s.list(func(r db.DevServerRow) bool {
		switch r.Row.Status {
		case "starting", "running", "stopping", "orphaned":
			return true
		}
		return false
	}), nil
}

// ListByRepo implements devservers.Store.
func (s *Store) ListByRepo(_ context.Context, repo core.RepoID) ([]db.DevServerRow, error) {
	return s.list(func(r db.DevServerRow) bool { return r.Row.Key.Repo == repo }), nil
}

var _ devservers.Store = (*Store)(nil)

// FakeClock is a devservers.Clock whose time moves only with Advance.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *FakeClock
	at   time.Time
	f    func()
	done bool
}

// NewClock returns a clock at a fixed time.
func NewClock() *FakeClock {
	return &FakeClock{now: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
}

// Now implements devservers.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc implements devservers.Clock. f runs inside Advance.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) devservers.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

// Pending is the number of timers that have not fired or been stopped.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves time forward by d and runs every timer that falls due, in
// time order, on the caller's goroutine (timers they add may run too).
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.done && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// Repos is a fake devservers.Repos: a fixed map from target to folder.
type Repos struct {
	mu   sync.Mutex
	dirs map[devservers.Target]string
}

// NewRepos returns an empty fake.
func NewRepos() *Repos { return &Repos{dirs: map[devservers.Target]string{}} }

// Track makes target resolve to dir.
func (r *Repos) Track(t devservers.Target, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs[t] = dir
}

// Untrack removes a target.
func (r *Repos) Untrack(t devservers.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dirs, t)
}

// Dir implements devservers.Repos.
func (r *Repos) Dir(_ context.Context, t devservers.Target) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.dirs[t]
	if !ok {
		return "", apperr.New(apperr.NotFound, "That repo or worktree is not tracked.", nil)
	}
	return d, nil
}

// Trust is a fake devservers.TrustSource: the view per repo (TrustNone default).
type Trust struct {
	mu    sync.Mutex
	views map[core.RepoID]guard.TrustView
	Err   error
}

// Set sets the view of a repo.
func (t *Trust) Set(repo core.RepoID, v guard.TrustView) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.views == nil {
		t.views = map[core.RepoID]guard.TrustView{}
	}
	t.views[repo] = v
}

// TrustView implements devservers.TrustSource.
func (t *Trust) TrustView(_ context.Context, repo core.RepoID) (guard.TrustView, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return guard.TrustNone, t.Err
	}
	if v, ok := t.views[repo]; ok {
		return v, nil
	}
	return guard.TrustNone, nil
}

// Templates is a fake devservers.Templates keyed by command.
type Templates struct {
	mu   sync.Mutex
	cmds map[string]Command
}

// Command is argv and env of a fake dev server command.
type Command struct {
	Argv []string
	Env  map[string]string
}

// Set offers command.
func (t *Templates) Set(command string, c Command) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmds == nil {
		t.cmds = map[string]Command{}
	}
	t.cmds[command] = c
}

// DevServer implements devservers.Templates.
func (t *Templates) DevServer(_ context.Context, _ devservers.Target, command string) ([]string, map[string]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.cmds[command]
	if !ok {
		return nil, nil, apperr.New(apperr.Invalid, "That command is not offered for this repo.", nil)
	}
	return slices.Clone(c.Argv), c.Env, nil
}

// Blocks is a fake devservers.BlockSource: the same block per worktree
// (allocated in order from Base).
type Blocks struct {
	mu   sync.Mutex
	Base int
	got  map[core.WorktreeID]core.PortBlock
	Err  error
}

// BlockFor implements devservers.BlockSource.
func (b *Blocks) BlockFor(_ context.Context, t devservers.Target) (core.PortBlock, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return core.PortBlock{}, b.Err
	}
	if t.Worktree == "" {
		return core.PortBlock{}, nil
	}
	if b.got == nil {
		b.got = map[core.WorktreeID]core.PortBlock{}
	}
	if blk, ok := b.got[t.Worktree]; ok {
		return blk, nil
	}
	base := b.Base
	if base == 0 {
		base = 20000
	}
	blk := core.PortBlock{Base: base + len(b.got)*core.PortBlockSize, Size: core.PortBlockSize}
	b.got[t.Worktree] = blk
	return blk, nil
}

// Hub records what the service publishes.
type Hub struct {
	mu      sync.Mutex
	Servers []devservers.View
	Lines   map[string][]devservers.LogLine
}

// PublishServer implements devservers.Publisher.
func (h *Hub) PublishServer(_ string, v devservers.View) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Servers = append(h.Servers, v)
}

// PublishLog implements devservers.Publisher.
func (h *Hub) PublishLog(id string, line devservers.LogLine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Lines == nil {
		h.Lines = map[string][]devservers.LogLine{}
	}
	h.Lines[id] = append(h.Lines[id], line)
}

// Statuses returns the statuses published so far.
func (h *Hub) Statuses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.Servers))
	for i, v := range h.Servers {
		out[i] = v.Status
	}
	return out
}

var _ devservers.Publisher = (*Hub)(nil)

// LinesOf returns the log lines published for a server.
func (h *Hub) LinesOf(id string) []devservers.LogLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.Lines[id])
}
