package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// fakeHostCache is the cached host data of a repo with a failed pipeline. It
// has no network and no token.
type fakeHostCache struct{ failed core.RepoID }

func (f fakeHostCache) RepoHost(_ context.Context, repo core.RepoID) (api.RepoHostView, error) {
	if repo != f.failed {
		return api.RepoHostView{NotLinked: "no host"}, nil
	}
	return api.RepoHostView{
		Linked: true, Instance: corehosts.Instance{ID: "github.com", Kind: corehosts.KindGitHub, Token: true}, Project: "acme/shop-api",
		Entry: corehosts.Entry{Phase: corehosts.Idle{Data: corehosts.RepoState{Pipeline: &corehosts.Pipeline{
			Status: corehosts.PipelineFailed, URL: "https://github.com/acme/shop-api/actions/runs/9001"}}}},
	}, nil
}

func (f fakeHostCache) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	return map[core.RepoID]struct{}{f.failed: {}}, nil
}

type noHostReader struct{}

func (noHostReader) Read(context.Context, corehosts.ContextKey) (hosts.ContextOutcome, error) {
	return hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}, nil
}

// waitTaskSettled waits until the task has left queued and running.
func waitTaskSettled(t *testing.T, r *p6Rig, id core.TaskID) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		task, err := r.a.store.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != core.TaskQueued && task.Status != core.TaskRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s is still %s", id, task.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPhase6NeedsAttentionActions is exit criterion E6 end to end over a
// seeded temp store: a repo in each Needs-attention state gets at least one
// offering from the real catalogue through the real HTTP handler, and starting
// an Ask-mode action never changes a working tree.
func TestPhase6NeedsAttentionActions(t *testing.T) {
	r := newP6Rig(t)
	ctx := context.Background()

	unreadable := r.newRepo(t)
	conflicts := r.newRepo(t)
	diverged := r.newRepo(t)
	behind := r.newRepo(t)
	dirty := r.newRepo(t)
	ci := r.newRepo(t)
	build := r.newRepo(t)
	all := []*p6Repo{unreadable, conflicts, diverged, behind, dirty, ci, build}

	r.remoteCommit(t, diverged)
	r.localCommit(t, diverged, "local.txt")
	r.remoteCommit(t, behind)
	if err := os.WriteFile(filepath.Join(dirty.dir, "notes.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unmerged path: three index stages of one file.
	blob := func(s string) string {
		return searchtest.GitOutput(t, r.env, conflicts.dir, s, "hash-object", "-w", "--stdin")
	}
	idx := "100644 " + blob("base\n") + " 1\tc.txt\n100644 " + blob("ours\n") + " 2\tc.txt\n100644 " + blob("theirs\n") + " 3\tc.txt\n"
	searchtest.GitOutput(t, r.env, conflicts.dir, idx, "update-index", "--index-info")
	// A failed local build of one repo.
	if err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
		started := time.Now().UTC().Add(-time.Minute)
		now := time.Now().UTC()
		_, err := tx.SaveBuild(ctx, corebuilds.Finished{
			ID: "bld_p6failed", Spec: corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: build.id, Name: "verify", Argv: []string{"false"}}}},
			QueuedAt: started, Actor: "user", StartedAt: &started, FinishedAt: now,
			Steps:  []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: started, FinishedAt: now, Outcome: corebuilds.Failed{ExitCode: 1}}},
			Result: corebuilds.ResultFailed{Step: 0},
		}, db.NewBuild)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, all...)
	if err := os.RemoveAll(unreadable.dir); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, all...)

	// The failed pipeline comes from the cached host data: bind an action
	// service over a scope whose host cache has one (no network, no token).
	scope := &scopeReader{store: r.a.store, status: r.a.status, hosts: fakeHostCache{failed: ci.id}, git: r.a.git}
	svc, err := actions.New(actions.Deps{
		Scope: scope, Context: &contextReader{scope: scope, host: noHostReader{}, store: r.a.store, logs: r.a.builds, git: r.a.git, redact: redactorForTest()},
		Tasks: r.a.tasks, Proposals: &proposalCreator{svc: r.a.proposals, drafts: r.a.tasks, store: r.a.store}, Requests: r.a.store,
		Hub: r.a.srv, Drafts: folderDrafts{svc: r.a.tasks}, Redactor: redactorForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	r.a.srv.SetPhase6Services(api.Phase6Services{Actions: actionsAPI{svc: svc}})

	want := map[*p6Repo]api.AttentionReason{
		unreadable: api.ReasonUnreadable, conflicts: api.ReasonConflicts, diverged: api.ReasonDiverged, behind: api.ReasonBehind,
		dirty: api.ReasonDirty, ci: api.ReasonFailedCI, build: api.ReasonFailedBuild,
	}
	for repo, reason := range want {
		primary, _ := coreactions.PrimaryKind(coreactions.Reason(reason))
		found := false
		for _, o := range r.catalogue(t, repo).Offerings {
			if o.Reason == reason && o.Title != "" && o.Kind == api.ActionKind(primary) {
				found = true
			}
		}
		if !found {
			t.Errorf("reason %s: no %s offering in %+v", reason, primary, r.catalogue(t, repo).Offerings)
		}
	}
	if o := r.offering(t, ci, api.ActionDiagnosePipeline); o.SourceID != "9001" {
		t.Errorf("the pipeline offering pins %q, want run 9001", o.SourceID)
	}

	// Starting an Ask action on a dirty repo changes nothing in its tree.
	before := r.tree(t, dirty)
	o := r.offering(t, dirty, api.ActionProposeCommits)
	if o.Mode != core.ModeAsk {
		t.Fatalf("mode = %q, want ask", o.Mode)
	}
	_, res := r.execute(t, api.ActionRequestDTO{RequestID: "req_commits_1", RepoID: dirty.id, Kind: o.Kind, Agent: "mock"})
	if res.State != api.ActionStateCompleted || res.TaskID == "" {
		t.Fatalf("result = %+v", res)
	}
	waitTaskSettled(t, r, res.TaskID)
	if after := r.tree(t, dirty); after != before {
		t.Fatalf("an Ask action changed the tree:\n%s\n%s", before, after)
	}
}
