package proposals

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/mainlock/mainlocktest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const (
	testToken = "hunter2-token-value-9f8e7d"
	remoteURL = "https://gitlab.example.test/acme/shop-api.git"
	branch    = "ygg/swift-fern"
	wtID      = core.WorktreeID("wt_one")
)

// spyGit wraps the fake git writer: it counts calls in flight per directory,
// can slow commits down and can panic, and records every request.
type spyGit struct {
	*gittest.Fake
	mu        sync.Mutex
	inflight  map[string]int
	maxFlight int
	delay     time.Duration
	panicOn   string
	commits   []string // messages, in the order commits ran
	pushes    []git.PushRequest
	adds      [][]string
	branches  map[string]string // CurrentBranch answers
	// loseCommitResult makes the next Commit land and then time out.
	loseCommitResult bool
	// extraStaged are paths someone staged outside the proposal.
	extraStaged []string
}

// CurrentBranch answers from a table the test sets; the fake writer does not
// report the checked-out branch of a folder.
func (g *spyGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.branches[dir], nil
}

// StagedPaths answers with what Add staged plus anything a test staged by hand.
func (g *spyGit) StagedPaths(_ context.Context, dir string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := append([]string(nil), g.extraStaged...)
	for _, a := range g.adds {
		out = append(out, a...)
	}
	return out, nil
}

func (g *spyGit) setCurrentBranch(dir, b string) {
	g.mu.Lock()
	g.branches[dir] = b
	g.mu.Unlock()
}

func (g *spyGit) enter(dir string) func() {
	g.mu.Lock()
	g.inflight[dir]++
	g.maxFlight = max(g.maxFlight, g.inflight[dir])
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.inflight[dir]--; g.mu.Unlock() }
}

func (g *spyGit) Add(ctx context.Context, dir string, paths []string) error {
	g.mu.Lock()
	g.adds = append(g.adds, append([]string(nil), paths...))
	g.mu.Unlock()
	return g.Fake.Add(ctx, dir, paths)
}

func (g *spyGit) Commit(ctx context.Context, dir string, paths []string, message, expect string) (string, error) {
	defer g.enter(dir)()
	if g.panicOn == "commit" {
		panic("boom in commit")
	}
	time.Sleep(g.delay)
	g.mu.Lock()
	g.commits = append(g.commits, message)
	g.mu.Unlock()
	id, err := g.Fake.Commit(ctx, dir, paths, message, expect)
	if err == nil && g.loseCommitResult {
		g.loseCommitResult = false
		return "", context.DeadlineExceeded // landed, but the caller never heard
	}
	return id, err
}

func (g *spyGit) Push(ctx context.Context, dir string, req git.PushRequest) (git.PushResult, error) {
	defer g.enter(dir)()
	time.Sleep(g.delay)
	g.mu.Lock()
	g.pushes = append(g.pushes, req)
	g.mu.Unlock()
	return g.Fake.Push(ctx, dir, req)
}

func (g *spyGit) commitCount() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.commits) }

type fakeResolver struct {
	mu        sync.Mutex
	repo      RepoInfo
	wts       map[core.WorktreeID]WorktreeInfo
	task      TaskInfo
	untracked bool
}

func (r *fakeResolver) Repo(_ context.Context, id core.RepoID) (RepoInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.repo.ID {
		return RepoInfo{}, db.ErrNotFound
	}
	info := r.repo
	info.Tracked = !r.untracked
	return info, nil
}

func (r *fakeResolver) Worktree(_ context.Context, id core.WorktreeID) (WorktreeInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wts[id]
	if !ok {
		return WorktreeInfo{}, db.ErrNotFound
	}
	return w, nil
}

func (r *fakeResolver) Task(_ context.Context, id core.TaskID) (TaskInfo, error) {
	if id != r.task.ID {
		return TaskInfo{}, db.ErrNotFound
	}
	return r.task, nil
}

func (r *fakeResolver) setUntracked(v bool) { r.mu.Lock(); r.untracked = v; r.mu.Unlock() }

type fakeBuilds struct {
	mu      sync.Mutex
	owners  []string
	steps   []string
	outcome BuildOutcome
	live    map[string]core.BuildID // owner -> a build still queued or running
	known   []string                // install steps the repo has; nil: any
	inWait chan struct{} // closed when Wait is entered, if set
	release chan struct{} // Wait blocks until it is closed, if set
}

func (b *fakeBuilds) Enqueue(_ context.Context, _ core.RepoID, step, owner string) (core.BuildID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owners, b.steps = append(b.owners, owner), append(b.steps, step)
	return "bld_one", nil
}

// FindLive answers from a table the test sets.
func (b *fakeBuilds) FindLive(_ context.Context, owner string) (core.BuildID, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.live[owner]
	return id, ok, nil
}

// HasInstallStep says whether step is in known (every step when known is nil).
func (b *fakeBuilds) HasInstallStep(_ context.Context, _ core.RepoID, step string) (bool, error) {
	return b.known == nil || slices.Contains(b.known, step), nil
}

func (b *fakeBuilds) Wait(ctx context.Context, _ core.BuildID) (BuildOutcome, error) {
	if b.inWait != nil {
		close(b.inWait)
	}
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return BuildOutcome{}, ctx.Err()
		}
	}
	return b.outcome, nil
}

type fakeHosts struct{}

func (fakeHosts) Kind(u string) HostKind {
	switch {
	case strings.Contains(u, "gitlab"):
		return HostGitLab
	case strings.Contains(u, "github"):
		return HostGitHub
	}
	return HostUnknown
}

type recPublisher struct {
	mu     sync.Mutex
	events []Event
}

func (p *recPublisher) Publish(e Event) { p.mu.Lock(); p.events = append(p.events, e); p.mu.Unlock() }

type world struct {
	t       *testing.T
	store   *db.Store
	svc     *Service
	git     *spyGit
	builds  *fakeBuilds
	lock    *mainlocktest.Fake
	res     *fakeResolver
	pub     *recPublisher
	repo    core.Repo
	task    core.Task
	wtDir   string
	mainDir string
	cfg     Config
	ctx     context.Context
}

// newWorld builds a service on a real database, a fake git writer, the fake
// lock and real temp folders for the worktree and the main checkout. The
// service is started.
func newWorld(t *testing.T, redactSecrets ...string) *world {
	t.Helper()
	ctx := context.Background()
	red := safety.NewRedactor(redactSecrets...)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, red.Redact)
	proj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: filepath.Join(t.TempDir(), "acme-shop")})
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: proj.ID, Name: "shop-api", DefaultBranch: "main"})
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	task := core.Task{ID: "tsk_one", ProjectID: proj.ID, Title: "t", Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repo.ID},
		Mode: core.ModeCode, Agent: "mock", CreatedAt: now, UpdatedAt: now}
	wtDir, mainDir := filepath.Join(t.TempDir(), "wt"), filepath.Join(t.TempDir(), "main")
	for _, d := range []string{wtDir, mainDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.CreateTask(ctx, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fg := gittest.NewFake()
	fg.AddRepo(wtDir, branch)
	fg.AddRemote(wtDir, "origin", remoteURL)
	fg.AddRepo(mainDir, "main")
	fg.AddRemote(mainDir, "origin", remoteURL)
	w := &world{
		t: t, store: store, git: &spyGit{Fake: fg, inflight: map[string]int{}, branches: map[string]string{mainDir: "main", wtDir: branch}}, builds: &fakeBuilds{outcome: BuildOutcome{Succeeded: true}},
		lock: &mainlocktest.Fake{}, pub: &recPublisher{}, repo: repo, task: task, wtDir: wtDir, mainDir: mainDir, ctx: ctx,
		res: &fakeResolver{
			repo: RepoInfo{ID: repo.ID, Path: mainDir, Name: repo.Name},
			wts:  map[core.WorktreeID]WorktreeInfo{wtID: {ID: wtID, Repo: repo.ID, Task: task.ID, Path: wtDir, Branch: branch}},
			task: TaskInfo{ID: task.ID, Repos: []core.RepoID{repo.ID}},
		},
	}
	var tick int64
	var tmu sync.Mutex
	w.cfg = Config{
		Store: store, Git: w.git, Resolver: w.res, Lock: w.lock, Builds: w.builds, Hosts: fakeHosts{}, Publisher: w.pub,
		Redactor: red,
		Clock: func() time.Time {
			tmu.Lock()
			defer tmu.Unlock()
			tick++
			return now.Add(time.Duration(tick) * time.Second)
		},
		ExecTimeout: time.Minute,
	}
	w.start()
	return w
}

// start (re)creates the service on the same store and starts it.
func (w *world) start() {
	w.t.Helper()
	svc, err := New(w.cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := svc.Start(w.ctx); err != nil {
		w.t.Fatal(err)
	}
	w.svc = svc
	w.t.Cleanup(svc.Close)
}

// switchBranch moves the worktree to a branch in the fake and in the resolver.
func (w *world) switchBranch(b string) {
	w.git.SetBranch(w.wtDir, b)
	w.res.mu.Lock()
	info := w.res.wts[wtID]
	info.Branch = b
	w.res.wts[wtID] = info
	w.res.mu.Unlock()
}

func (w *world) head(dir string) string {
	w.t.Helper()
	h, err := w.git.Head(w.ctx, dir)
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

// commitAction writes a change in the worktree and returns a Commit action for
// it with the right ExpectHead and ExpectDiffHash.
func (w *world) commitAction(file, message string) coreprop.Commit {
	w.t.Helper()
	w.git.Write(w.wtDir, file, "content of "+file+"\n")
	dh, err := w.git.DiffHash(w.ctx, w.wtDir, []string{file})
	if err != nil {
		w.t.Fatal(err)
	}
	return coreprop.Commit{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{coreprop.RelPath(file)},
		Message: message, ExpectHead: w.head(w.wtDir), ExpectDiffHash: dh}
}

func (w *world) create(a coreprop.Action) View {
	w.t.Helper()
	v, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: a, Summary: "because", Source: coreprop.AgentSource{Task: w.task.ID}})
	if err != nil {
		w.t.Fatalf("create %T: %v", a, err)
	}
	return v
}

// waitFor polls until the proposal has the status.
func (w *world) waitFor(id core.ProposalID, status core.ProposalStatus) View {
	w.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := w.svc.Get(w.ctx, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if v.Status == status {
			return v
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("proposal %s is %s (%s), want %s", id, v.Status, v.Message, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (w *world) auditActions(id core.ProposalID) []string {
	w.t.Helper()
	rows, err := w.store.ListAudit(w.ctx, "proposal:"+string(id), 100)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- { // newest first
		out = append(out, rows[i].Action)
	}
	return out
}

func gitPush(head string) git.PushRequest {
	return git.PushRequest{Remote: "origin", Branch: branch, ExpectHead: head}
}

func mainlockBuild(id string) mainlock.Owner {
	return mainlock.Owner{Kind: mainlock.KindBuild, ID: id}
}
