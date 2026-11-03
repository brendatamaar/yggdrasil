package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// changeSetLinks feeds the MR description of the next repo with the links of
// the repos before it (RV-03). Its facts must come only from this change
// set's merge_request members: the branch from every MR proposal, a URL only
// from an executed one.
func TestChangeSetLinksReadsOnlyExecutedMRsOfThisSet(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor()
	store := db.NewStore(d, red.Redact)

	proj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
	mkRepo := func(name string) core.Repo {
		return dbtest.Repo(t, store, core.Repo{ProjectID: proj.ID, Name: name, Path: filepath.Join(proj.Path, name)})
	}
	api, web, admin, other := mkRepo("shop-api"), mkRepo("shop-web"), mkRepo("shop-admin"), mkRepo("shop-docs")
	task := dbtest.Task(t, store, core.Task{ProjectID: proj.ID, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{api.ID, web.ID, admin.ID}, Mode: core.ModeCode, Status: core.TaskReview})
	task2 := dbtest.Task(t, store, core.Task{ProjectID: proj.ID, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{other.ID}, Mode: core.ModeCode, Status: core.TaskReview})

	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	n := 0
	head, h64 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	add := func(tk core.TaskID, repo core.Repo, a coreprop.Action, state coreprop.State) (core.ProposalID, core.ChangeSetID) {
		t.Helper()
		n++
		id := core.ProposalID("prp_" + string(rune('a'+n)))
		var set core.ChangeSetID
		err := store.Commit(ctx, func(tx *db.Tx) error {
			cs, err := tx.EnsureChangeSet(ctx, tk, core.ChangeSetID("cs_"+string(id)), now)
			if err != nil {
				return err
			}
			set = cs
			p, _ := coreprop.Step(coreprop.Proposal{}, coreprop.Created{
				Proposal: coreprop.Proposal{ID: id, ChangeSet: cs, Task: tk, Repo: repo.ID, Source: coreprop.UserSource{}, Action: a, Summary: "s"},
				Facts:    coreprop.Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, TaskChangeSet: cs},
				Now:      now,
			})
			if p.State == nil {
				t.Fatalf("Step refused %T", a)
			}
			if err := tx.CreateProposalRecord(ctx, p); err != nil {
				return err
			}
			if state != nil {
				p.State = state
				_, err = tx.SaveProposalRecord(ctx, p, 0)
			}
			return err
		})
		if err != nil {
			t.Fatalf("add %T: %v", a, err)
		}
		return id, set
	}
	mr := func(branch string) coreprop.MergeRequest {
		return coreprop.MergeRequest{Where: coreprop.InWorktree{ID: "wt_1"}, Branch: branch, Remote: "origin", TargetBranch: "main", Title: "t", Description: "d", ExpectHead: head}
	}
	exec := func(url string) coreprop.State {
		return coreprop.Executed{At: now, Result: coreprop.Result{MRURL: url}}
	}
	_, set := add(task.ID, api, mr("ygg/api"), exec("https://gitlab.example.test/acme/shop-api/-/merge_requests/7"))
	add(task.ID, web, mr("ygg/web"), nil) // pending: branch yes, URL no
	add(task.ID, admin, mr("ygg/admin"), coreprop.Failed{Attempt: 1, At: now, Kind: coreprop.FailExec, Message: "push failed", Retryable: true})
	// An executed non-MR member with a URL in its result is not an MR link.
	add(task.ID, web, coreprop.Push{Where: coreprop.InWorktree{ID: "wt_1"}, Branch: "ygg/web", Remote: "origin", ExpectHead: head}, exec("https://gitlab.example.test/acme/shop-web/-/merge_requests/99"))
	_ = h64
	// Another task's set: executed MR in its own repo, must not show up.
	_, set2 := add(task2.ID, other, mr("ygg/docs"), exec("https://gitlab.example.test/acme/shop-docs/-/merge_requests/3"))

	// Merge order is stored on the set: web first, then api, then admin.
	order := []core.RepoID{web.ID, api.ID, admin.ID}
	if err := store.Commit(ctx, func(tx *db.Tx) error {
		row, err := tx.LoadChangeSetRow(ctx, set)
		if err != nil {
			return err
		}
		row.MergeOrder = order
		_, err = tx.SaveChangeSetRun(ctx, row, row.Version)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	l := changeSetLinks{store: store, red: red}
	got, err := l.MRLinks(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reposToStrings(got.Order), ",") != strings.Join(reposToStrings(order), ",") {
		t.Errorf("Order = %v, want %v", got.Order, order)
	}
	wantBranches := map[core.RepoID]string{api.ID: "ygg/api", web.ID: "ygg/web", admin.ID: "ygg/admin"}
	if len(got.Branches) != len(wantBranches) {
		t.Errorf("Branches = %v, want %v", got.Branches, wantBranches)
	}
	for r, b := range wantBranches {
		if got.Branches[r] != b {
			t.Errorf("Branches[%s] = %q, want %q", r, got.Branches[r], b)
		}
	}
	if len(got.URLs) != 1 || got.URLs[api.ID] != "https://gitlab.example.test/acme/shop-api/-/merge_requests/7" {
		t.Errorf("URLs = %v; want only the executed MR of shop-api (not the pending, failed, push or other-set ones)", got.URLs)
	}
	if _, ok := got.Branches[other.ID]; ok {
		t.Error("a branch of another change set leaked in")
	}

	// The other set sees only its own.
	got2, err := l.MRLinks(ctx, set2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.URLs) != 1 || got2.URLs[other.ID] == "" || len(got2.Branches) != 1 || got2.Branches[other.ID] != "ygg/docs" {
		t.Errorf("second set links = %+v", got2)
	}
	if _, err := l.MRLinks(ctx, "cs_missing"); err == nil {
		t.Error("an unknown change set was accepted")
	}
}

func reposToStrings(r []core.RepoID) []string {
	out := make([]string, len(r))
	for i, x := range r {
		out[i] = string(x)
	}
	return out
}
