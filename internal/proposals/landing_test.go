package proposals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/mainlock/mainlocktest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// fakeMarks records worktree status changes and mirrors them into the resolver.
type fakeMarks struct {
	mu  sync.Mutex
	res *fakeResolver
	log []string
}

func (m *fakeMarks) set(id core.WorktreeID, st core.WorktreeStatus, tag string) {
	m.mu.Lock()
	m.log = append(m.log, tag)
	m.mu.Unlock()
	m.res.mu.Lock()
	w := m.res.wts[id]
	w.Status = st
	m.res.wts[id] = w
	m.res.mu.Unlock()
}

func (m *fakeMarks) MarkMerged(_ context.Context, _ *db.Tx, id core.WorktreeID) error {
	m.set(id, core.WorktreeMerged, "merged")
	return nil
}

func (m *fakeMarks) MarkRemoving(_ context.Context, _ *db.Tx, id core.WorktreeID, leaf string) error {
	m.set(id, core.WorktreeRemoving, "removing:"+leaf)
	return nil
}

func (m *fakeMarks) MarkRemoved(_ context.Context, _ *db.Tx, id core.WorktreeID) error {
	m.set(id, core.WorktreeRemoved, "removed")
	return nil
}

func (m *fakeMarks) entries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.log)
}

type fakeLive struct{ task, build bool }

func (f *fakeLive) TaskLive(context.Context, core.TaskID) (bool, error)      { return f.task, nil }
func (f *fakeLive) BuildLive(context.Context, core.WorktreeID) (bool, error) { return f.build, nil }

// spyLanding is the real platform with scripted move failures and a record of
// what it was asked to delete and whose lock holders it looked up.
type spyLanding struct {
	platform.RealLanding
	mu          sync.Mutex
	failMove    *platform.MoveError
	holders     []string
	holderCalls int
	removed     []string
	moves       int
}

func (l *spyLanding) MoveDir(from, to string) error {
	l.mu.Lock()
	l.moves++
	f := l.failMove
	l.mu.Unlock()
	if f != nil {
		return f
	}
	return l.RealLanding.MoveDir(from, to)
}

func (l *spyLanding) RemoveStaged(root, staged string) (platform.RemoveReport, error) {
	l.mu.Lock()
	l.removed = append(l.removed, staged)
	l.mu.Unlock()
	return l.RealLanding.RemoveStaged(root, staged)
}

func (l *spyLanding) LockHolders(path string) []string {
	l.mu.Lock()
	l.holderCalls++
	h := l.holders
	l.mu.Unlock()
	if h != nil {
		return h
	}
	return l.RealLanding.LockHolders(path)
}

func (l *spyLanding) setFail(f *platform.MoveError) { l.mu.Lock(); l.failMove = f; l.mu.Unlock() }

// pushSpy counts pushes: a landing run must never make one.
type pushSpy struct {
	*git.Git
	mu     sync.Mutex
	pushes int
}

func (g *pushSpy) Push(ctx context.Context, dir string, req git.PushRequest) (git.PushResult, error) {
	g.mu.Lock()
	g.pushes++
	g.mu.Unlock()
	return g.Git.Push(ctx, dir, req)
}

type landWorld struct {
	t     *testing.T
	ctx   context.Context
	svc   *Service
	g     *pushSpy
	h     gittest.LandingHarness
	res   *fakeResolver
	marks *fakeMarks
	live  *fakeLive
	plat  *spyLanding
	lock  *mainlocktest.Fake
	store *db.Store
	task  core.TaskID
	repo  core.RepoID
	root  string
	main  string
}

func newLandWorld(t *testing.T) *landWorld {
	t.Helper()
	ctx := context.Background()
	_, h := gittest.NewRealLandingWorld(t)
	cfgDir := t.TempDir()
	gcfg := filepath.Join(cfgDir, "gitconfig")
	if err := os.WriteFile(gcfg, []byte("[user]\n\tname = Ada Test\n\temail = ada@example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{"GIT_CONFIG_GLOBAL": gcfg, "GIT_CONFIG_NOSYSTEM": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	real, err := git.New(env)
	if err != nil {
		t.Skip("git is not installed")
	}
	g := &pushSpy{Git: real}
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, nil)
	proj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: filepath.Join(t.TempDir(), "acme-shop")})
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: proj.ID, Name: "shop-api", DefaultBranch: "main"})
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	task := core.Task{ID: "tsk_land", ProjectID: proj.ID, Title: "t", Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repo.ID},
		Mode: core.ModeCode, Agent: "mock", CreatedAt: now, UpdatedAt: now}
	if err := store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.CreateTask(ctx, task); return err }); err != nil {
		t.Fatal(err)
	}
	res := &fakeResolver{
		repo: RepoInfo{ID: repo.ID, Path: h.Dir(), Name: repo.Name},
		wts:  map[core.WorktreeID]WorktreeInfo{},
		task: TaskInfo{ID: task.ID, Repos: []core.RepoID{repo.ID}},
	}
	w := &landWorld{t: t, ctx: ctx, g: g, h: h, res: res, marks: &fakeMarks{res: res}, live: &fakeLive{}, plat: &spyLanding{},
		lock: &mainlocktest.Fake{}, store: store, task: task.ID, repo: repo.ID, root: filepath.Dir(h.Dir()), main: h.Dir()}
	svc, err := New(Config{Store: store, Git: g, Resolver: res, Lock: w.lock, Builds: &fakeBuilds{outcome: BuildOutcome{Succeeded: true}},
		Hosts: fakeHosts{}, ExecTimeout: 40 * time.Second, Marks: w.marks, Live: w.live, Platform: w.plat, WorktreeRoot: w.root})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	w.svc = svc
	return w
}

// worktree adds a linked worktree with one commit and registers it.
func (w *landWorld) worktree(name string, commit bool) (id core.WorktreeID, dir, tip string) {
	w.t.Helper()
	dir, tip = w.h.Worktree(name)
	if commit {
		w.h.WriteFile(dir, name+".txt", "from "+name+"\n")
		tip = w.h.CommitAll(dir, "add "+name)
	}
	id = core.WorktreeID("wt_" + name)
	w.res.mu.Lock()
	w.res.wts[id] = WorktreeInfo{ID: id, Repo: w.repo, Task: w.task, Path: dir, Branch: "ygg/" + name, Name: "wt-" + name, Status: core.WorktreeActive}
	w.res.mu.Unlock()
	return id, dir, tip
}

func (w *landWorld) merge(id core.WorktreeID, name, expect string) coreprop.MergeLocal {
	return coreprop.MergeLocal{Where: coreprop.MainCheckout{Repo: w.repo}, Worktree: id, Branch: "ygg/" + name,
		TargetBranch: "main", ExpectBranchHead: expect, Title: "Add " + name}
}

func (w *landWorld) create(a coreprop.Action) View {
	w.t.Helper()
	v, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task, Action: a, Summary: "because", Source: coreprop.AgentSource{Task: w.task}})
	if err != nil {
		w.t.Fatalf("create %T: %v", a, err)
	}
	return v
}

func (w *landWorld) settle(id core.ProposalID, want core.ProposalStatus) View {
	w.t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		got, err := w.svc.Get(w.ctx, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if got.Status == want {
			return got
		}
		if (got.Status == core.ProposalFailed || got.Status == core.ProposalExecuted) || time.Now().After(deadline) {
			w.t.Fatalf("proposal is %s (%s: %s), want %s", got.Status, got.FailKind, got.Message, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *landWorld) run(a coreprop.Action, want core.ProposalStatus) View {
	w.t.Helper()
	v := w.create(a)
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		w.t.Fatal(err)
	}
	return w.settle(v.ID, want)
}

func (w *landWorld) head() string {
	w.t.Helper()
	h, err := w.g.Head(w.ctx, w.main)
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

func (w *landWorld) noPush() {
	w.t.Helper()
	if w.g.pushes != 0 {
		w.t.Fatalf("a landing run pushed %d time(s)", w.g.pushes)
	}
}

func TestMergeLocalMakesAMergeCommitAndPushesNothing(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("a", true)
	remote := w.h.RemoteRefs()
	got := w.run(w.merge(id, "a", tip), core.ProposalExecuted)
	head := w.head()
	if got.Result.CommitID != head {
		t.Fatalf("result commit %q, HEAD %q", got.Result.CommitID, head)
	}
	if ps := w.h.Parents(head); len(ps) != 2 || !slices.Contains(ps, tip) {
		t.Fatalf("parents of the merge commit = %v, want two including %s", ps, tip)
	}
	if body, ok := w.h.ReadFile(w.main, "a.txt"); !ok || body != "from a\n" {
		t.Fatalf("a.txt in the main checkout = %q, %v", body, ok)
	}
	if !w.h.TreeUnchanged(w.main) {
		t.Fatal("the main checkout is not clean after the merge")
	}
	if !slices.Equal(w.marks.entries(), []string{"merged"}) {
		t.Fatalf("marks = %v", w.marks.entries())
	}
	w.noPush()
	if now := w.h.RemoteRefs(); !mapsEqual(now, remote) {
		t.Fatalf("remote changed: %v -> %v", remote, now)
	}
	acts := audits(t, w, got.ID)
	if !slices.Contains(acts, "landing.merge") {
		t.Fatalf("no landing.merge audit entry: %v", acts)
	}
	// Reconcile (read only) sees the merge as landed.
	if out, _ := w.svc.reconcileMerge(w.ctx, coreprop.Proposal{Repo: w.repo, Task: w.task}, w.merge(id, "a", tip)); out != coreprop.ReconcileDone {
		t.Fatalf("reconcile after a landed merge = %v", out)
	}
}

func audits(t *testing.T, w *landWorld, id core.ProposalID) []string {
	t.Helper()
	rows, err := w.store.ListAudit(w.ctx, "proposal:"+string(id), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestMergeLocalConflictLeavesTheCheckoutUntouched(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("c", false)
	w.h.WriteFile(dir, "README.md", "from the worktree\n")
	tip := w.h.CommitAll(dir, "change readme")
	w.h.CommitMain("README.md", "from main\n")
	before := w.head()
	got := w.run(w.merge(id, "c", tip), core.ProposalFailed)
	if !strings.Contains(got.Message, "README.md") {
		t.Fatalf("message does not name the file: %q", got.Message)
	}
	if got.Retryable {
		t.Fatal("a conflict must not offer a plain retry")
	}
	if w.head() != before || !w.h.TreeUnchanged(w.main) {
		t.Fatal("the main checkout changed")
	}
	if len(w.marks.entries()) != 0 {
		t.Fatalf("worktree was marked: %v", w.marks.entries())
	}
	w.noPush()
}

func TestMergeLocalRefusesADirtyMainCheckoutWithoutTouchingIt(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("d", true)
	w.h.WriteFile(w.main, "scratch.txt", "mine\n") // untracked counts as dirty
	before := w.head()
	got := w.run(w.merge(id, "d", tip), core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	if w.head() != before {
		t.Fatal("HEAD moved")
	}
	if body, ok := w.h.ReadFile(w.main, "scratch.txt"); !ok || body != "mine\n" {
		t.Fatal("the user's file was touched")
	}
}

func TestMergeLocalAlreadyMergedMarksTheWorktree(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("e", false) // branch == main: already part of it
	before := w.head()
	got := w.run(w.merge(id, "e", tip), core.ProposalExecuted)
	if w.head() != before {
		t.Fatal("HEAD moved for an already merged branch")
	}
	if !strings.Contains(got.Result.Note, "already") || !slices.Equal(w.marks.entries(), []string{"merged"}) {
		t.Fatalf("result %+v, marks %v", got.Result, w.marks.entries())
	}
}

func TestMergeLocalStaleBranchHeadIsNeverMerged(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, tip := w.worktree("s", true)
	v := w.create(w.merge(id, "s", tip))
	w.h.WriteFile(dir, "more.txt", "x\n")
	w.h.CommitAll(dir, "more")
	before := w.head()
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		t.Fatal(err)
	}
	got := w.settle(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) || w.head() != before {
		t.Fatalf("failure %+v, HEAD moved: %v", got, w.head() != before)
	}
}

func TestMergeLocalUntrackedRepoRefused(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("u", true)
	v := w.create(w.merge(id, "u", tip))
	w.res.setUntracked(true)
	before := w.head()
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err == nil {
		got := w.settle(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailRepoUntracked) {
			t.Fatalf("failure %+v", got)
		}
	}
	if w.head() != before {
		t.Fatal("HEAD moved in an untracked repo")
	}
}

func TestMergeLocalWaitsForTheMainCheckoutLock(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("l", true)
	release, _, ok := w.lock.TryAcquire([]core.RepoID{w.repo}, mainlock.Owner{Kind: mainlock.KindBuild, ID: "b1"})
	if !ok {
		t.Fatal("could not take the lock")
	}
	before := w.head()
	got := w.run(w.merge(id, "l", tip), core.ProposalFailed)
	if !got.Retryable || !strings.Contains(got.Message, "build") || w.head() != before {
		t.Fatalf("failure %+v", got)
	}
	release()
}

// A merge that a crash left in progress: a retry (attempt 2) is "recovering"
// and aborts it only when it is our own (MERGE_HEAD is the proposed head).
func TestMergeLocalRecoversItsOwnInterruptedMerge(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("r", false)
	w.h.WriteFile(dir, "README.md", "from the worktree\n")
	tip := w.h.CommitAll(dir, "change readme")
	w.h.CommitMain("README.md", "from main\n")
	before := w.head()
	v := w.create(w.merge(id, "r", tip))
	// The "crash": the merge started and left conflicts in the main checkout.
	var ce *git.MergeConflictError
	if _, err := w.g.MergeNoFF(w.ctx, w.main, tip, "Merge branch 'ygg/r'"); !errors.As(err, &ce) {
		t.Fatalf("setup merge: %v", err)
	}
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		t.Fatal(err)
	}
	first := w.settle(v.ID, core.ProposalFailed) // attempt 1: fresh, merge in progress: refused
	if !first.Retryable || w.h.TreeUnchanged(w.main) {
		t.Fatalf("first attempt %+v, tree clean=%v (the merge in progress must stay)", first, w.h.TreeUnchanged(w.main))
	}
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		g, _ := w.svc.Get(w.ctx, v.ID)
		return g.Status == core.ProposalFailed && g.FailKind == string(coreprop.FailInterrupted)
	})
	if !w.h.TreeUnchanged(w.main) || w.head() != before {
		t.Fatal("recovery did not restore the main checkout")
	}
	w.noPush()
}

func TestMergeLocalDoesNotAbortSomeoneElsesMerge(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("x", true)
	// The user's own merge of another branch is in progress.
	otherID, odir, _ := w.worktree("other", false)
	_ = otherID
	w.h.WriteFile(odir, "README.md", "other\n")
	otherTip := w.h.CommitAll(odir, "other readme")
	w.h.CommitMain("README.md", "main\n")
	if _, err := w.g.MergeNoFF(w.ctx, w.main, otherTip, "user merge"); err == nil {
		t.Fatal("expected a conflict")
	}
	v := w.create(w.merge(id, "x", tip))
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		t.Fatal(err)
	}
	w.settle(v.ID, core.ProposalFailed)
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	waitFor(t, func() bool { g, _ := w.svc.Get(w.ctx, v.ID); return g.Status == core.ProposalFailed })
	if inProg, mh, _ := w.g.MergeInProgress(w.ctx, w.main); !inProg || mh != otherTip {
		t.Fatalf("the user's merge was aborted (in progress=%v, head %q)", inProg, mh)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoveWorktreeStagesThenDeletesAndKeepsTheBranch(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, tip := w.worktree("g", true)
	canary := filepath.Join(w.main, "canary.txt")
	if err := os.WriteFile(canary, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalExecuted)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the worktree folder is still there: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(w.root, landingStagingDir)); len(left) != 0 {
		t.Fatalf("staging folder not empty: %v", left)
	}
	if bh, err := w.g.BranchHead(w.ctx, w.main, "ygg/g"); err != nil || bh != tip {
		t.Fatalf("the branch is gone or moved: %q %v", bh, err)
	}
	list, _ := w.g.WorktreeList(w.ctx, w.main)
	for _, wt := range list {
		if strings.EqualFold(wt.Path, dir) {
			t.Fatalf("git still lists the removed worktree: %+v", wt)
		}
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatal("a file outside the worktree was deleted")
	}
	m := w.marks.entries()
	if len(m) != 2 || !strings.HasPrefix(m[0], "removing:wt-g-") || m[1] != "removed" {
		t.Fatalf("marks = %v", m)
	}
	canon, err := platform.ResolveRoot(w.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.plat.removed) == 0 {
		t.Fatal("nothing was deleted")
	}
	for _, p := range w.plat.removed {
		if !strings.HasPrefix(p, filepath.Join(canon, landingStagingDir)+string(filepath.Separator)) {
			t.Fatalf("RemoveStaged was given %q", p)
		}
	}
	if w.plat.holderCalls != 0 {
		t.Fatal("lock holders were looked up although nothing failed")
	}
	w.noPush()
}

const landingStagingDir = ".removing"

func TestRemoveWorktreeRefusesUncommittedWork(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("h", true)
	w.h.WriteFile(dir, "wip.txt", "unsaved\n")
	got := w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) {
		t.Fatalf("failure %+v", got)
	}
	if body, ok := w.h.ReadFile(dir, "wip.txt"); !ok || body != "unsaved\n" {
		t.Fatal("the worktree was touched")
	}
	if w.plat.moves != 0 || len(w.marks.entries()) != 0 {
		t.Fatal("something was moved or marked")
	}
}

func TestRemoveWorktreeRefusesARunningTaskOrBuild(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("i", true)
	w.live.task = true
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	w.live.task, w.live.build = false, true
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if _, err := os.Stat(dir); err != nil || w.plat.moves != 0 {
		t.Fatal("the worktree was touched")
	}
}

func TestRemoveWorktreeRefusesAMainCheckoutAndAFolderOutsideTheRoot(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	w.res.mu.Lock()
	w.res.wts["wt_main"] = WorktreeInfo{ID: "wt_main", Repo: w.repo, Task: w.task, Path: w.main, Branch: "main", Name: "work", Status: core.WorktreeActive}
	w.res.mu.Unlock()
	w.run(coreprop.RemoveWorktree{Worktree: "wt_main"}, core.ProposalFailed)
	outside := t.TempDir()
	w.res.mu.Lock()
	w.res.wts["wt_out"] = WorktreeInfo{ID: "wt_out", Repo: w.repo, Task: w.task, Path: outside, Branch: "x", Name: "out", Status: core.WorktreeActive}
	w.res.mu.Unlock()
	w.run(coreprop.RemoveWorktree{Worktree: "wt_out"}, core.ProposalFailed)
	for _, d := range []string{w.main, outside} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was removed", d)
		}
	}
	if w.plat.moves != 0 {
		t.Fatal("a move was attempted")
	}
}

func TestRemoveWorktreeLockedIsRetryableNamesTheHolderAndChangesNothing(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("k", true)
	w.plat.setFail(&platform.MoveError{Class: platform.LockSharing, Err: errors.New("in use")})
	w.plat.holders = []string{"code.exe", "pwsh.exe"}
	got := w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if !got.Retryable || !strings.Contains(got.Message, "code.exe") || !strings.Contains(got.Message, "Nothing was changed") {
		t.Fatalf("failure %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "k.txt")); err != nil {
		t.Fatal("the worktree changed")
	}
	if len(w.marks.entries()) != 0 {
		t.Fatalf("marks = %v", w.marks.entries())
	}
	if w.plat.holderCalls != 1 {
		t.Fatalf("holders looked up %d times, want once after the failed move", w.plat.holderCalls)
	}
	// The user closes the program; Retry succeeds. Nothing was killed.
	w.plat.setFail(nil)
	if _, err := w.svc.Retry(w.ctx, got.ID, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g, _ := w.svc.Get(w.ctx, got.ID); return g.Status == core.ProposalExecuted })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the worktree is still there after the retry")
	}
}

// A run that stopped after the rename leaves a staged folder; the next run
// resumes the delete from the observation and ends removed.
func TestRemoveWorktreeResumesAHalfStagedRemoval(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("m", true)
	staged := filepath.Join(w.root, landingStagingDir, "wt-m-oldrun")
	if err := os.MkdirAll(filepath.Dir(staged), 0o750); err != nil {
		t.Fatal(err)
	}
	rootCanon, err := platform.ResolveRoot(w.root)
	if err != nil {
		t.Fatal(err)
	}
	staged = filepath.Join(rootCanon, landingStagingDir, "wt-m-oldrun")
	if err := os.Rename(dir, staged); err != nil {
		t.Fatal(err)
	}
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalExecuted)
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staged folder is still there")
	}
	if m := w.marks.entries(); len(m) == 0 || m[len(m)-1] != "removed" {
		t.Fatalf("marks = %v", m)
	}
}

// A removing row with nothing on disk ends removed.
func TestRemoveWorktreeRowRemovingWithNothingOnDiskEndsRemoved(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("n", true)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	w.res.mu.Lock()
	wt := w.res.wts[id]
	wt.Status = core.WorktreeRemoving
	w.res.wts[id] = wt
	w.res.mu.Unlock()
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalExecuted)
	if m := w.marks.entries(); !slices.Equal(m, []string{"removed"}) {
		t.Fatalf("marks = %v", m)
	}
}

func TestRemoveWorktreeUntrackedRepoRefused(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("o", true)
	v := w.create(coreprop.RemoveWorktree{Worktree: id})
	w.res.setUntracked(true)
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err == nil {
		w.settle(v.ID, core.ProposalFailed)
	}
	if _, err := os.Stat(dir); err != nil || w.plat.moves != 0 {
		t.Fatal("the worktree was touched in an untracked repo")
	}
}

func TestLandingKindsQueueWithCommitsOfTheSameCheckout(t *testing.T) {
	t.Parallel()
	main := coreprop.MainCheckout{Repo: "rp_one"}
	commitKey := keyOf(coreprop.Commit{Where: main})
	if k := keyOf(coreprop.MergeLocal{Where: main}); k != commitKey {
		t.Fatalf("merge key %q, commit key %q", k, commitKey)
	}
	p := coreprop.Proposal{Repo: "rp_one"}
	if k := queueKey(p, coreprop.RemoveWorktree{Worktree: "wt_x"}); k != commitKey {
		t.Fatalf("remove key %q, commit key %q", k, commitKey)
	}
}

// Safety: the agent never gets these two kinds (invariant 2).
func TestMCPDoesNotOfferMergeLocalOrRemoveWorktree(t *testing.T) {
	t.Parallel()
	for _, tool := range cmcp.AllTools() {
		n := strings.ToLower(string(tool))
		if strings.Contains(n, "merge_local") || strings.Contains(n, "remove_worktree") {
			t.Fatalf("MCP offers %q", tool)
		}
	}
}
