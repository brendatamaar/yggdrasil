package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Locator tells the service where repos and worktrees live. The lead wires an
// adapter over the db store; tests use a map.
type Locator interface {
	// RepoDir is the main checkout of a tracked repo.
	RepoDir(ctx context.Context, id core.RepoID) (string, error)
	// WorktreeDir is the folder of a task worktree.
	WorktreeDir(ctx context.Context, id core.WorktreeID) (string, error)
	// Known lists the tracked repos and worktrees, for ParseRequest.
	Known(ctx context.Context) (Known, error)
}

// Publisher sends build changes and live log lines to the UI. *api.Server
// implements it.
type Publisher interface {
	PublishBuildChanged(id core.BuildID, status core.BuildStatus)
	PublishBuildLog(id core.BuildID, step, attempt int, line string)
}

// StatusRefresher asks the repo status service for a new read. It is
// optional; *repos.StatusService satisfies it.
type StatusRefresher interface {
	Refresh(ctx context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error
}

// ResolveFunc is the signature of platform.Resolve.
type ResolveFunc func(name, dir string, env []string) (string, error)

// Deps are what the service needs. Store, Start, Locator and Env are
// required; everything else has a default.
type Deps struct {
	Store   BuildStore
	Start   platform.StartFunc
	Resolve ResolveFunc // default platform.Resolve
	Locator Locator
	Env     safety.EnvBuilder
	// Logs defaults to NewFileLogs(DataDir/logs/builds, Redactor, Publisher).
	Logs LogSink
	// Artifacts is the finder used after a successful build. Default: the
	// toolchains' globs over OpenFS.
	Artifacts ArtifactFinder
	// Toolchains are the detectable modules (toolchains.All()).
	Toolchains []Toolchain
	// OpenFS returns the file system of a folder. Default os.DirFS.
	OpenFS func(dir string) fs.FS
	// DirExists says whether a step folder exists. Default: os.Stat.
	DirExists func(dir string) bool
	Clock     func() time.Time // default time.Now().UTC()
	IDs       func(prefix string) string
	Redactor  safety.Redactor // default safety.NewRedactor()
	Publisher Publisher       // optional
	Status    StatusRefresher // optional
	// ReconcileBackoff is the first wait between retries of the startup
	// ListActive; it doubles per retry. Default 200 ms.
	ReconcileBackoff time.Duration
	DataDir          string
	Log              *slog.Logger
}

// BuildStarted is sent to subscribers when a build is admitted.
type BuildStarted struct {
	ID     core.BuildID
	Target corebuilds.Target
	Repos  []core.RepoID
}

// Service runs builds (docs/design/builds.md). Every input of every build goes
// through one owner goroutine; effects run only after the state is committed.
//
// Wiring in cmd/ygg (the lead): build Deps (Store: NewSQLiteStore(store),
// Start: platform.Start, Locator over the db store, Env: safety env builder,
// Toolchains: toolchains.All(), DataDir, Publisher: the api server, Status:
// the status service), call NewService, add svc.Start(ctx) to the app errgroup
// (it blocks until ctx is done and does the ProcessLost reconciliation first),
// and pass the service as api.Deps.Builds.
type Service struct {
	d Deps

	inbox chan msg
	wake  chan struct{}
	done  chan struct{} // closed when the owner goroutine ends

	mu      sync.Mutex
	g       *errgroup.Group
	ctx     context.Context
	live    map[stepKey]context.CancelFunc
	started map[stepKey]struct{}
	subs    []func(BuildStarted)
}

type stepKey struct {
	build   core.BuildID
	index   int
	attempt int
}

type msg struct {
	id    core.BuildID
	in    corebuilds.Input
	reply chan<- result // nil for fire-and-forget inputs
}

type result struct {
	state   corebuilds.State
	changed bool
	err     error
}

// NewService checks deps and returns the service. Nothing runs until Start.
func NewService(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("builds: a store is required")
	case d.Start == nil:
		return nil, errors.New("builds: a start function is required")
	case d.Locator == nil:
		return nil, errors.New("builds: a locator is required")
	case d.Env == nil:
		return nil, errors.New("builds: an environment builder is required")
	}
	if d.Resolve == nil {
		d.Resolve = platform.Resolve
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Redactor == nil {
		d.Redactor = safety.NewRedactor()
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.OpenFS == nil {
		d.OpenFS = func(dir string) fs.FS { return os.DirFS(dir) }
	}
	if d.DirExists == nil {
		d.DirExists = func(dir string) bool {
			fi, err := os.Stat(dir)
			return err == nil && fi.IsDir()
		}
	}
	if d.Logs == nil {
		if d.DataDir == "" {
			return nil, errors.New("builds: a data directory (or a log sink) is required")
		}
		d.Logs = NewFileLogs(logRoot(d.DataDir), d.Redactor, d.Publisher)
	}
	if d.Artifacts == nil {
		d.Artifacts = ToolchainArtifacts{Toolchains: d.Toolchains}
	}
	return &Service{
		d: d, inbox: make(chan msg, 64), wake: make(chan struct{}, 1), done: make(chan struct{}),
		live: map[stepKey]context.CancelFunc{}, started: map[stepKey]struct{}{},
	}, nil
}

// SubscribeStarted registers fn, called on the owner goroutine each time a
// build is admitted (git actions in B3 use it to refuse a commit while a
// build runs). fn must not block. Call it before Start.
func (s *Service) SubscribeStarted(fn func(BuildStarted)) { s.subs = append(s.subs, fn) }

// Start runs the owner goroutine and the queue worker until ctx is done. It
// first feeds ProcessLost for every build left Running or Cancelling by a
// previous run, then wakes the queue. It returns nil when ctx ends.
func (s *Service) Start(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	s.mu.Lock()
	s.g, s.ctx = g, gctx
	s.mu.Unlock()
	g.Go(func() error { s.owner(gctx); return nil })
	g.Go(func() error { s.worker(gctx); return nil })
	return g.Wait()
}

// owner is the one goroutine that turns inputs into committed state.
func (s *Service) owner(ctx context.Context) {
	defer close(s.done)
	s.reconcile(ctx)
	s.signalWake()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.inbox:
			if ctx.Err() != nil {
				return
			}
			s.process(ctx, m)
		}
	}
}

// reconcile marks builds that were running when ygg died as interrupted: the
// job object killed their processes with ygg (S2).
//
// A failing ListActive is retried with a bounded backoff. When the store
// returns some builds together with an error (one corrupt row), the parseable
// ones are still reconciled and the error is logged.
func (s *Service) reconcile(ctx context.Context) {
	const attempts = 6
	delay := s.d.ReconcileBackoff
	if delay <= 0 {
		delay = 200 * time.Millisecond
	}
	var active []corebuilds.State
	for try := 1; ; try++ {
		var err error
		active, err = s.d.Store.ListActive(ctx)
		if err == nil {
			break
		}
		if len(active) > 0 {
			s.d.Log.Error("list active builds at startup: some rows could not be read and were skipped", "err", err)
			break
		}
		if try == attempts || ctx.Err() != nil {
			s.d.Log.Error("list active builds at startup: giving up, interrupted builds stay Running until the next start", "err", err, "attempts", try)
			return
		}
		s.d.Log.Warn("list active builds at startup failed, retrying", "err", err, "attempt", try)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Second)
	}
	for _, st := range active {
		id, ok := stateID(st)
		if !ok {
			continue
		}
		s.process(ctx, msg{id: id, in: corebuilds.ProcessLost{Now: s.d.Clock()}})
	}
}

// process handles one message and the follow-up inputs its commands produce.
func (s *Service) process(ctx context.Context, m msg) {
	queue := []msg{m}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		res, follow := s.safeHandle(ctx, cur)
		if cur.reply != nil {
			cur.reply <- res
		}
		queue = append(queue, follow...)
	}
}

func (s *Service) safeHandle(ctx context.Context, m msg) (res result, follow []msg) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("builds: invariant broken, input dropped", "build", m.id, "input", fmt.Sprintf("%T", m.in), "panic", r)
			res = result{err: errors.New("builds: internal error")}
			follow = nil
		}
	}()
	return s.handle(ctx, m)
}

// handle is `handle(buildID, input)` of the design: commit, then interpret.
func (s *Service) handle(ctx context.Context, m msg) (result, []msg) {
	before, after, cmds, err := s.commit(ctx, m.id, m.in)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return result{err: apperr.New(apperr.NotFound, "That build doesn't exist.", err)}, nil
		}
		if errors.Is(err, db.ErrConflict) {
			s.d.Log.Error("builds: version conflict after retry", "build", m.id, "err", err)
		} else {
			s.d.Log.Error("builds: commit failed", "build", m.id, "err", err)
		}
		return result{err: apperr.New(apperr.Unavailable, "Yggdrasil couldn't save the build. Try again.", err)}, nil
	}
	if len(cmds) == 0 {
		return result{state: after}, nil
	}
	follow, perr := s.safeInterpret(ctx, m.id, before, after, cmds)
	if perr != nil {
		if _, again := m.in.(corebuilds.ProcessLost); again {
			follow = nil // the recovery itself panicked: do not loop
		}
		return result{state: after, changed: true, err: errors.New("builds: internal error")}, follow
	}
	return result{state: after, changed: true}, follow
}

// safeInterpret runs interpret and, if it panics, logs which commands were
// committed but not (fully) performed. The state is already saved.
func (s *Service) safeInterpret(ctx context.Context, id core.BuildID, before, after corebuilds.State, cmds []corebuilds.Command) (follow []msg, perr any) {
	defer func() {
		if r := recover(); r != nil {
			types := make([]string, len(cmds))
			for i, c := range cmds {
				types[i] = fmt.Sprintf("%T", c)
			}
			s.d.Log.Error("builds: interpreting commands panicked, some commands were lost", "build", id, "commands", types, "panic", r)
			// The remaining commands are lost. Recover through the core: a
			// build left Running without a process ends Interrupted (like a
			// restart would), and a Finished one ignores the input. Wake the
			// queue so the next build can start.
			follow = []msg{{id: id, in: corebuilds.ProcessLost{Now: s.d.Clock()}}}
			perr = r
			s.killBuild(id)
			s.signalWake()
		}
	}()
	return s.interpret(ctx, id, before, after, cmds), nil
}

// killBuild cancels every live step process of a build, so a build that the
// recovery ends is left with no process (S4).
func (s *Service) killBuild(id core.BuildID) {
	s.mu.Lock()
	var cancels []context.CancelFunc
	for k, c := range s.live {
		if k.build == id {
			cancels = append(cancels, c)
		}
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// commit loads, steps, saves and audits in one transaction, with one retry on
// a version conflict.
func (s *Service) commit(ctx context.Context, id core.BuildID, in corebuilds.Input) (before, after corebuilds.State, cmds []corebuilds.Command, err error) {
	for try := 0; try < 2; try++ {
		before, after, cmds = nil, nil, nil
		err = s.d.Store.Commit(ctx, func(tx BuildTx) error {
			st, ver, lerr := tx.LoadBuild(ctx, id)
			if lerr != nil {
				if _, isEnqueue := in.(corebuilds.Enqueue); !isEnqueue || !errors.Is(lerr, db.ErrNotFound) {
					return lerr
				}
				st, ver = corebuilds.None{}, NewBuild
			}
			next, out := corebuilds.Step(st, in)
			before, after, cmds = st, next, out
			if len(out) == 0 {
				return nil
			}
			if _, serr := tx.SaveBuild(ctx, next, ver); serr != nil {
				return serr
			}
			for _, c := range out {
				w, ok := c.(corebuilds.WriteAudit)
				if !ok {
					continue
				}
				if aerr := s.appendAudit(ctx, tx, w); aerr != nil {
					return aerr
				}
			}
			return nil
		})
		if err == nil || !errors.Is(err, db.ErrConflict) {
			break
		}
	}
	return before, after, cmds, err
}

func (s *Service) appendAudit(ctx context.Context, tx BuildTx, w corebuilds.WriteAudit) error {
	detail := json.RawMessage("{}")
	if len(w.Detail) > 0 {
		b, err := json.Marshal(w.Detail)
		if err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		detail = b
	}
	actor := string(w.Actor)
	if actor == "" {
		actor = "system"
	}
	e, err := audit.Prepare(core.AuditEntry{Actor: actor, Action: w.Action, Subject: w.Subject, Detail: detail},
		s.d.Redactor, core.AuditID(s.d.IDs("aud")), s.d.Clock())
	if err != nil {
		return fmt.Errorf("prepare audit entry: %w", err)
	}
	return tx.AppendAudit(ctx, e)
}

var errStopped = apperr.New(apperr.Unavailable, "Yggdrasil is shutting down. Try again after it restarts.", errors.New("builds service stopped"))

// send submits an input from any goroutine and waits for its result.
func (s *Service) send(ctx context.Context, id core.BuildID, in corebuilds.Input) (result, error) {
	reply := make(chan result, 1)
	select {
	case s.inbox <- msg{id: id, in: in, reply: reply}:
	case <-ctx.Done():
		return result{}, ctx.Err()
	case <-s.done:
		return result{}, errStopped
	}
	select {
	case r := <-reply:
		return r, r.err
	case <-s.done:
		return result{}, errStopped
	}
}

// post submits an input without waiting (used by goroutines that report a
// process result). It gives up when the service stops.
func (s *Service) post(id core.BuildID, in corebuilds.Input) {
	select {
	case s.inbox <- msg{id: id, in: in}:
	case <-s.done:
	}
}

func (s *Service) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Enqueue stores a new queued build and wakes the queue. It returns once the
// build is committed.
func (s *Service) Enqueue(ctx context.Context, spec corebuilds.Spec, actor corebuilds.Actor) (core.BuildID, error) {
	if err := spec.Validate(); err != nil {
		return "", invalid("The build is not valid.", err)
	}
	if err := actor.Validate(); err != nil {
		return "", invalid("The build has no valid actor.", err)
	}
	id := core.BuildID(s.d.IDs("bld"))
	if _, err := s.send(ctx, id, corebuilds.Enqueue{ID: id, Spec: spec, Actor: actor, Now: s.d.Clock()}); err != nil {
		return "", err
	}
	return id, nil
}

// EnqueueRequest parses a POST /api/builds body against the tracked repos and
// enqueues it.
func (s *Service) EnqueueRequest(ctx context.Context, raw []byte, actor corebuilds.Actor) (core.BuildID, error) {
	known, err := s.d.Locator.Known(ctx)
	if err != nil {
		return "", unavailable("Yggdrasil couldn't read the tracked repos. Try again.", err)
	}
	spec, err := ParseRequest(raw, known)
	if err != nil {
		return "", err
	}
	return s.Enqueue(ctx, spec, actor)
}

// Cancel asks a build to stop. It is idempotent.
func (s *Service) Cancel(ctx context.Context, id core.BuildID, actor corebuilds.Actor) error {
	if err := id.Validate(); err != nil {
		return invalid("That isn't a build ID.", err)
	}
	if err := actor.Validate(); err != nil {
		return invalid("The cancel has no valid actor.", err)
	}
	_, err := s.send(ctx, id, corebuilds.Cancel{Actor: actor, Now: s.d.Clock()})
	return err
}

func unavailable(msg string, err error) error { return apperr.New(apperr.Unavailable, msg, err) }

func storeErr(err error, what string) error {
	switch {
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That "+what+" doesn't exist.", err)
	case errors.Is(err, db.ErrConflict):
		return apperr.New(apperr.Conflict, "The "+what+" changed in the meantime. Reload and try again.", err)
	}
	return unavailable("Yggdrasil couldn't read the "+what+". Try again.", err)
}

// Get returns the state of one build.
func (s *Service) Get(ctx context.Context, id core.BuildID) (corebuilds.State, error) {
	if err := id.Validate(); err != nil {
		return nil, invalid("That isn't a build ID.", err)
	}
	st, _, err := s.d.Store.Get(ctx, id)
	if err != nil {
		return nil, storeErr(err, "build")
	}
	return st, nil
}

// List returns builds, newest first.
func (s *Service) List(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error) {
	out, err := s.d.Store.List(ctx, f)
	if err != nil {
		return nil, storeErr(err, "build list")
	}
	return out, nil
}

// BusyMainCheckouts says which of the repos have a queued or running build in
// their main checkout, with a plain-language reason. Git actions in B3 use it.
func (s *Service) BusyMainCheckouts(ctx context.Context, ids []core.RepoID) (map[core.RepoID]string, error) {
	const running, queued = "a build is running in this repo", "a build is queued for this repo"
	busy := map[core.RepoID]string{}
	want := map[core.RepoID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	mark := func(st corebuilds.State, reason string) {
		spec, ok := specOf(st)
		if !ok {
			return
		}
		if _, main := spec.Target.(corebuilds.MainCheckout); !main {
			return
		}
		for _, step := range spec.Steps {
			if want[step.RepoID] && busy[step.RepoID] != running {
				busy[step.RepoID] = reason
			}
		}
	}
	active, err := s.d.Store.ListActive(ctx)
	if err != nil {
		return nil, storeErr(err, "build list")
	}
	for _, st := range active {
		mark(st, running)
	}
	refs, err := s.d.Store.ListQueued(ctx)
	if err != nil {
		return nil, storeErr(err, "build list")
	}
	for _, q := range refs {
		st, _, gerr := s.d.Store.Get(ctx, q.ID)
		if gerr != nil {
			continue
		}
		mark(st, queued)
	}
	return busy, nil
}

func stateID(st corebuilds.State) (core.BuildID, bool) {
	switch x := st.(type) {
	case corebuilds.Queued:
		return x.ID, true
	case corebuilds.Running:
		return x.ID, true
	case corebuilds.Cancelling:
		return x.ID, true
	case corebuilds.Finished:
		return x.ID, true
	}
	return "", false
}

func specOf(st corebuilds.State) (corebuilds.Spec, bool) {
	switch x := st.(type) {
	case corebuilds.Queued:
		return x.Spec, true
	case corebuilds.Running:
		return x.Spec, true
	case corebuilds.Cancelling:
		return x.Spec, true
	case corebuilds.Finished:
		return x.Spec, true
	}
	return corebuilds.Spec{}, false
}

func uniqueRepos(spec corebuilds.Spec) []core.RepoID {
	var out []core.RepoID
	for _, st := range spec.Steps {
		if !slices.Contains(out, st.RepoID) {
			out = append(out, st.RepoID)
		}
	}
	return out
}
