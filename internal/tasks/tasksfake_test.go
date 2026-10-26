package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/worktrees/worktreestest"
)

// ---- fakes of the injected capabilities ----

// scriptAdapter replays a mock fixture and lets a test shape the run: block it
// on a gate, change files while it "runs", fail, tamper, or leave unhooked
// tool uses. It counts live runs so a test can check that no run outlives a
// finished task.
type scriptAdapter struct {
	caps    agents.Capabilities
	fixture string

	// gate, when set, holds every run after its first event until it is closed
	// or the run is cancelled.
	gate chan struct{}
	// during runs in the run goroutine while the agent "works".
	during func(opts agents.StartOptions)
	// startPanic makes Start panic.
	startPanic bool
	startErr   error
	tamper     bool
	unhooked   []string
	denies     []agents.Deny
	denyCount  int
	checkErr   error
	configs    []string

	mu      sync.Mutex
	started []agents.StartOptions
	live    atomic.Int32
	maxLive atomic.Int32
}

func newScriptAdapter() *scriptAdapter {
	return &scriptAdapter{caps: mock.New(mock.Options{}).Capabilities(), fixture: "plain"}
}

func (a *scriptAdapter) Name() string                      { return "mock" }
func (a *scriptAdapter) Capabilities() agents.Capabilities { return a.caps }
func (a *scriptAdapter) Check(context.Context) error       { return a.checkErr }
func (a *scriptAdapter) ConfigPaths() []string             { return a.configs }
func (a *scriptAdapter) Starts() []agents.StartOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agents.StartOptions(nil), a.started...)
}

func (a *scriptAdapter) Start(ctx context.Context, opts agents.StartOptions) (agents.Run, error) {
	if a.startPanic {
		panic("scripted adapter panic")
	}
	if a.startErr != nil {
		return nil, a.startErr
	}
	events, err := mock.Load(a.fixture)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.started = append(a.started, opts)
	a.mu.Unlock()
	n := a.live.Add(1)
	for {
		m := a.maxLive.Load()
		if n <= m || a.maxLive.CompareAndSwap(m, n) {
			break
		}
	}
	r := &scriptRun{events: make(chan agents.Event, len(events)+2), done: make(chan struct{}), a: a}
	r.session = "sess-" + string(opts.TaskID)
	go func() {
		defer a.live.Add(-1)
		defer close(r.done)
		defer close(r.events)
		cancelled := func() bool {
			if ctx.Err() == nil {
				return false
			}
			r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunCancelled, At: time.Now()}
			return true
		}
		for i, e := range events {
			if cancelled() {
				return
			}
			e.At = time.Now()
			r.events <- e
			if i == 0 {
				if a.during != nil {
					a.during(opts)
				}
				if a.gate != nil {
					select {
					case <-a.gate:
					case <-ctx.Done():
					}
				}
			}
		}
	}()
	return r, nil
}

type scriptRun struct {
	events  chan agents.Event
	done    chan struct{}
	session string
	a       *scriptAdapter
}

func (r *scriptRun) Events() <-chan agents.Event { return r.events }
func (r *scriptRun) SessionID() string           { return r.session }
func (r *scriptRun) Wait() error                 { <-r.done; return nil }
func (r *scriptRun) Tampered() bool              { return r.a.tamper }
func (r *scriptRun) HookedToolUses() []string    { return nil }
func (r *scriptRun) UnhookedToolUses() []string  { return r.a.unhooked }
func (r *scriptRun) Denies() []agents.Deny       { return r.a.denies }
func (r *scriptRun) DenyCount() int              { return max(r.a.denyCount, len(r.a.denies)) }

type registry map[string]agents.Adapter

func (r registry) Get(name string) (agents.Adapter, bool) { a, ok := r[name]; return a, ok }

type fakeHub struct {
	mu       sync.Mutex
	statuses []StatusMessage
	events   map[core.TaskID][]int64
}

func (h *fakeHub) Status(m StatusMessage) {
	h.mu.Lock()
	h.statuses = append(h.statuses, m)
	h.mu.Unlock()
}

func (h *fakeHub) Event(id core.TaskID, seq int64, _ agents.Event) {
	h.mu.Lock()
	if h.events == nil {
		h.events = map[core.TaskID][]int64{}
	}
	h.events[id] = append(h.events[id], seq)
	h.mu.Unlock()
}

func (h *fakeHub) statusesOf(id core.TaskID) []core.TaskStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []core.TaskStatus
	for _, m := range h.statuses {
		if m.TaskID == id {
			out = append(out, m.Status)
		}
	}
	return out
}

type fakeMachine struct{ cpus, mem int }

func (m fakeMachine) CPUs() int      { return m.cpus }
func (m fakeMachine) FreeMemMB() int { return m.mem }

type fakeKiller struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
}

func (k *fakeKiller) Kill(task string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.calls == nil {
		k.calls = map[string]int{}
	}
	k.calls[task]++
	return k.err
}

func (k *fakeKiller) count(task core.TaskID) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls[string(task)]
}

// fakeMainLock is an exclusive lock per repo. A test can hold a repo as a
// build would.
type fakeMainLock struct {
	mu     sync.Mutex
	owners map[core.RepoID]string
}

func (l *fakeMainLock) TryAcquire(repos []core.RepoID, owner string) (Release, string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owners == nil {
		l.owners = map[core.RepoID]string{}
	}
	for _, r := range repos {
		if o, held := l.owners[r]; held && o != owner {
			return nil, o, false
		}
	}
	for _, r := range repos {
		l.owners[r] = owner
	}
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, r := range repos {
			if l.owners[r] == owner {
				delete(l.owners, r)
			}
		}
	}, "", true
}

func (l *fakeMainLock) Held(repos []core.RepoID) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range repos {
		if o, ok := l.owners[r]; ok {
			return o, true
		}
	}
	return "", false
}

func (l *fakeMainLock) held(repo core.RepoID) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owners[repo]
}

// testScanner scans a map of repo dir -> files with the fake scanner's rules.
type testScanner struct {
	mu    sync.Mutex
	files map[string]map[string]string
	err   map[string]error
}

func (s *testScanner) set(dir string, files map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]map[string]string{}
	}
	s.files[dir] = files
}

func (s *testScanner) fail(dir string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = map[string]error{}
	}
	s.err[dir] = err
}

func (s *testScanner) Scan(ctx context.Context, dir string, paths, names []string) (guard.Scan, error) {
	s.mu.Lock()
	files, err := s.files[dir], s.err[dir]
	s.mu.Unlock()
	if err != nil {
		return guard.Scan{}, err
	}
	return safetytest.FakeScanner{Files: files}.Scan(ctx, dir, paths, names)
}

// testSnap fingerprints a folder (names and contents of every file). A test can
// replace fn to fail, block or hang.
type testSnap struct {
	mu sync.Mutex
	fn func(ctx context.Context, dir string) (guard.Snapshot, error)
	n  atomic.Int32
}

func (s *testSnap) set(fn func(ctx context.Context, dir string) (guard.Snapshot, error)) {
	s.mu.Lock()
	s.fn = fn
	s.mu.Unlock()
}

func (s *testSnap) Snapshot(ctx context.Context, dir string) (guard.Snapshot, error) {
	s.n.Add(1)
	s.mu.Lock()
	fn := s.fn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, dir)
	}
	return treeSnapshot(dir)
}

func treeSnapshot(dir string) (guard.Snapshot, error) {
	h := sha256.New()
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p) //nolint:gosec // test fake on a temp dir
		if err != nil {
			return err
		}
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return guard.Snapshot{}, err
	}
	sort.Strings(paths)
	sum := hex.EncodeToString(h.Sum(nil))
	return guard.Snapshot{Head: "head1", Status: sum, DiffHash: sum, Paths: paths}, nil
}

// slowWorktrees wraps a Worktrees and lets a test hold Ensure.
type slowWorktrees struct {
	inner Worktrees
	enter chan struct{} // closed-over signal: receives once when Ensure is entered
	hold  chan struct{} // Ensure waits for this to be closed when set
	calls atomic.Int32
	fail  error
	deaf  bool // Ensure ignores a cancelled context while it holds
	conc  atomic.Int32
	maxc  atomic.Int32
}

func (w *slowWorktrees) Ensure(ctx context.Context, task core.TaskID, repos []worktrees.RepoRef) ([]core.Worktree, error) {
	w.calls.Add(1)
	n := w.conc.Add(1)
	defer w.conc.Add(-1)
	for {
		m := w.maxc.Load()
		if n <= m || w.maxc.CompareAndSwap(m, n) {
			break
		}
	}
	if w.enter != nil {
		select {
		case w.enter <- struct{}{}:
		default:
		}
	}
	if w.hold != nil {
		if w.deaf {
			<-w.hold
		} else {
			select {
			case <-w.hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if w.fail != nil {
		return nil, w.fail
	}
	return w.inner.Ensure(ctx, task, repos)
}

// ---- harness ----

type harness struct {
	t       *testing.T
	svc     *Service
	cfg     Config
	store   *db.Store
	project core.Project
	repos   []core.Repo
	adapter *scriptAdapter
	hub     *fakeHub
	killer  *fakeKiller
	lock    *fakeMainLock
	scanner *testScanner
	snap    *testSnap
	wts     *slowWorktrees
	wtRoot  string
	// afterCommit is the crash hook of the service: returning true skips the
	// interpretation of the commands, as if the process died after the commit.
	afterCommit func(id core.TaskID, cmds []coretasks.Command) bool
	// beforeCommit makes the store fail for chosen inputs.
	beforeCommit func(id core.TaskID, in coretasks.Input) error
	// tune changes the Config before the service is built.
	tune func(*Config)
}

// restart closes the service and starts a new one on the same database, as
// after a crash and a restart.
func (h *harness) restart() *Service {
	h.t.Helper()
	_ = h.svc.Close()
	h.afterCommit, h.beforeCommit = nil, nil
	return h.start()
}

type opt func(*harness)

func newHarness(t *testing.T, repoNames []string, opts ...opt) *harness {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := db.NewStore(d, nil)
	h := &harness{t: t, store: st, adapter: newScriptAdapter(), hub: &fakeHub{}, killer: &fakeKiller{}, lock: &fakeMainLock{}, scanner: &testScanner{}, snap: &testSnap{}}
	root := t.TempDir()
	h.project = dbtest.Project(t, st, core.Project{Name: "acme-shop", Path: filepath.Join(root, "acme-shop")})
	for _, n := range repoNames {
		r := dbtest.Repo(t, st, core.Repo{ProjectID: h.project.ID, Name: n, Path: filepath.Join(h.project.Path, n), DefaultBranch: "main"})
		if err := os.MkdirAll(r.Path, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.Path, "README.md"), []byte("# "+n+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.repos = append(h.repos, r)
	}
	h.wtRoot = filepath.Join(root, "ygg")
	if err := os.MkdirAll(filepath.Join(h.wtRoot, "wt"), 0o750); err != nil {
		t.Fatal(err)
	}
	ens, err := worktrees.New(worktrees.Config{
		Git: worktreestest.NewGit(), Store: worktrees.DBStore{S: st}, Rand: &worktreestest.Rand{},
		Now: func() time.Time { return time.Now().UTC() }, NewID: db.NewID, DataDir: h.wtRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.wts = &slowWorktrees{inner: ens}
	for _, o := range opts {
		o(h)
	}
	return h
}

// start builds the service (once the options are applied) and starts it.
func (h *harness) start() *Service {
	h.t.Helper()
	h.cfg = Config{
		Store: h.store, Adapters: registry{"mock": h.adapter}, Worktrees: h.wts, MainLock: h.lock,
		Scanner: h.scanner, Snapshotter: h.snap, Env: safety.NewEnvBuilder(), Redactor: safety.NewRedactor(),
		Hub: h.hub, Machine: fakeMachine{cpus: 16, mem: 32768}, Killer: h.killer.Kill,
		Now: func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
		UsageEvery: time.Millisecond, SnapshotTimeout: 2 * time.Second, SnapshotRetry: time.Millisecond, LockRetry: 20 * time.Millisecond,
	}
	if h.tune != nil {
		h.tune(&h.cfg)
	}
	svc, err := New(h.cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	svc.afterCommit, svc.beforeCommit = h.afterCommit, h.beforeCommit
	if err := svc.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.svc = svc
	h.t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func (h *harness) msg(mode, prompt string, repo int) MessageRequest {
	return MessageRequest{
		ProjectID: h.project.ID, Prompt: prompt, Mode: mode, Scope: "repo", Repos: []string{string(h.repos[repo].ID)},
		Agent: "mock", OptIn: mode == "current_branch" || mode == "project",
	}
}

func (h *harness) send(req MessageRequest) MessageResult {
	h.t.Helper()
	res, err := h.svc.Message(context.Background(), req)
	if err != nil {
		h.t.Fatalf("message: %v", err)
	}
	return res
}

func (h *harness) state(id core.TaskID) db.TaskState {
	h.t.Helper()
	st, err := h.store.GetTaskState(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

// waitState polls until the task is in the wanted status.
func (h *harness) waitStatus(id core.TaskID, want core.TaskStatus) db.TaskState {
	h.t.Helper()
	var st db.TaskState
	waitFor(h.t, 10*time.Second, func() bool {
		var err error
		st, err = h.store.GetTaskState(context.Background(), id)
		return err == nil && st.Row.Status == want
	}, "task %s to become %s", id, want)
	return st
}

func (h *harness) audits(id core.TaskID) []string {
	h.t.Helper()
	list, err := h.store.ListAudit(context.Background(), "task:"+string(id), 500)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i].Action)
	}
	return out
}

func (h *harness) hasAudit(id core.TaskID, action string) bool {
	for _, a := range h.audits(id) {
		if a == action {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for "+format, args...)
}

var errBoom = errors.New("boom")

var _ = coretasks.DefaultMax
