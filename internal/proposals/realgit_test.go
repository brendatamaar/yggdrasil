package proposals

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/mainlock/mainlocktest"
)

// realWorld is the service on real git: a temp work repo on the task branch
// and a local bare remote. The service tests with the fake git skip all of
// this; here the executors run the real commit and push paths.
type realWorld struct {
	t   *testing.T
	ctx context.Context
	svc *Service
	g   git.Writer
	h   gittest.Harness
	res *fakeResolver
	tid core.TaskID
}

func newRealWorld(t *testing.T) *realWorld {
	t.Helper()
	ctx := context.Background()
	g, h := gittest.NewRealWorld(t)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, nil)
	proj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: filepath.Join(t.TempDir(), "acme-shop")})
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: proj.ID, Name: "shop-api", DefaultBranch: "main"})
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	task := core.Task{ID: "tsk_real", ProjectID: proj.ID, Title: "t", Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repo.ID},
		Mode: core.ModeCode, Agent: "mock", CreatedAt: now, UpdatedAt: now}
	if err := store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.CreateTask(ctx, task); return err }); err != nil {
		t.Fatal(err)
	}
	res := &fakeResolver{
		repo: RepoInfo{ID: repo.ID, Path: h.Dir()},
		wts:  map[core.WorktreeID]WorktreeInfo{wtID: {ID: wtID, Repo: repo.ID, Task: task.ID, Path: h.Dir(), Branch: h.Branch()}},
		task: TaskInfo{ID: task.ID, Repos: []core.RepoID{repo.ID}},
	}
	svc, err := New(Config{Store: store, Git: g, Resolver: res, Lock: &mainlocktest.Fake{},
		Builds: &fakeBuilds{outcome: BuildOutcome{Succeeded: true}}, Hosts: fakeHosts{}, ExecTimeout: 40 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &realWorld{t: t, ctx: ctx, svc: svc, g: g, h: h, res: res, tid: task.ID}
}

func (w *realWorld) create(a coreprop.Action) View {
	w.t.Helper()
	v, err := w.svc.Create(w.ctx, CreateRequest{Task: w.tid, Action: a, Summary: "because", Source: coreprop.AgentSource{Task: w.tid}})
	if err != nil {
		w.t.Fatalf("create %T: %v", a, err)
	}
	return v
}

func (w *realWorld) confirmAndWait(v View, want core.ProposalStatus) View {
	w.t.Helper()
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		w.t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	for {
		got, err := w.svc.Get(w.ctx, v.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		if got.Status == want {
			return got
		}
		if got.Status == core.ProposalFailed || got.Status == core.ProposalExecuted || time.Now().After(deadline) {
			w.t.Fatalf("proposal is %s (%s %s), want %s", got.Status, got.FailKind, got.Message, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *realWorld) commitAction(file, message string) coreprop.Commit {
	w.t.Helper()
	w.h.WriteFile(file, "content of "+file+"\n")
	head, err := w.g.Head(w.ctx, w.h.Dir())
	if err != nil {
		w.t.Fatal(err)
	}
	dh, err := w.g.DiffHash(w.ctx, w.h.Dir(), []string{file})
	if err != nil {
		w.t.Fatal(err)
	}
	return coreprop.Commit{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{coreprop.RelPath(file)},
		Message: message, ExpectHead: head, ExpectDiffHash: dh}
}

func TestRealGitCommitThenPushMovesTheRemoteAndRunsNoHooks(t *testing.T) {
	t.Parallel()
	w := newRealWorld(t)
	ran, ok := w.h.InstallHooks()
	if !ok {
		t.Skip("no hooks in this world")
	}
	v := w.create(w.commitAction("src/a.txt", "feat: add a"))
	got := w.confirmAndWait(v, core.ProposalExecuted)
	info, err := w.g.HeadInfo(w.ctx, w.h.Dir())
	if err != nil || info.ID != got.Result.CommitID || info.Message != "feat: add a" {
		t.Fatalf("HEAD = %+v (%v), result %+v", info, err, got.Result)
	}
	if ran() {
		t.Fatal("a hook ran during the proposal commit")
	}

	if w.h.RemoteTip(w.h.Branch()) != "" {
		t.Fatal("the remote had the branch before the push")
	}
	push := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: w.h.Branch(), Remote: "origin", ExpectHead: info.ID})
	w.confirmAndWait(push, core.ProposalExecuted)
	if tip := w.h.RemoteTip(w.h.Branch()); tip != info.ID {
		t.Fatalf("remote tip = %q, want %q", tip, info.ID)
	}
	if ran() {
		t.Fatal("a hook ran during the proposal push")
	}
}

func TestRealGitCommitOnAMovedHeadFailsStale(t *testing.T) {
	t.Parallel()
	w := newRealWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	// Someone else commits on the branch after the proposal was made.
	w.h.WriteFile("other.txt", "x\n")
	head, _ := w.g.Head(w.ctx, w.h.Dir())
	if err := w.g.Add(w.ctx, w.h.Dir(), []string{"other.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Commit(w.ctx, w.h.Dir(), []string{"other.txt"}, "other", head); err != nil {
		t.Fatal(err)
	}
	got := w.confirmAndWait(v, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) || got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
}

func TestRealGitPushToTheDefaultBranchIsRefused(t *testing.T) {
	t.Parallel()
	w := newRealWorld(t)
	// A commit ahead of the remote's main, so the push is not "already there".
	w.h.WriteFile("ahead.txt", "x\n")
	base, _ := w.g.Head(w.ctx, w.h.Dir())
	if err := w.g.Add(w.ctx, w.h.Dir(), []string{"ahead.txt"}); err != nil {
		t.Fatal(err)
	}
	head, err := w.g.Commit(w.ctx, w.h.Dir(), []string{"ahead.txt"}, "ahead", base)
	if err != nil {
		t.Fatal(err)
	}
	wt := w.res.wts[wtID]
	wt.Branch = "main"
	w.res.wts[wtID] = wt
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: "main", Remote: "origin", ExpectHead: head})
	before := w.h.RemoteTip("main")
	got := w.confirmAndWait(v, core.ProposalFailed)
	if got.Retryable || w.h.RemoteTip("main") != before {
		t.Fatalf("failure = %+v, remote main moved", got)
	}
}
