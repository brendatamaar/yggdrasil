package proposals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/changesets"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

type fakeLinks struct {
	sl  SetLinks
	err error
}

func (f fakeLinks) MRLinks(context.Context, core.ChangeSetID) (SetLinks, error) { return f.sl, f.err }

const (
	webURL   = "https://gitlab.example.test/acme/shop-web/-/merge_requests/7"
	adminURL = "https://gitlab.example.test/acme/shop-admin/-/merge_requests/9"
)

func (w *world) withLinks(l ChangeSetLinks) {
	w.t.Helper()
	w.svc.Close()
	w.cfg.Links = l
	w.start()
}

func (w *world) runMR(desc string) (View, coreprop.MergeRequest) {
	w.t.Helper()
	a := coreprop.MergeRequest{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", TargetBranch: "main",
		Title: "t", Description: desc, ExpectHead: w.head(w.wtDir)}
	v := w.create(a)
	w.confirm(v)
	return w.waitFor(v.ID, core.ProposalExecuted), a
}

func (w *world) pushedDescription() string {
	w.t.Helper()
	w.git.mu.Lock()
	defer w.git.mu.Unlock()
	for _, p := range w.git.pushes {
		if p.MR != nil {
			return p.MR.Description
		}
	}
	w.t.Fatal("no merge request push")
	return ""
}

func TestMRMarkerIsFilledWithEarlierMRsOnly(t *testing.T) {
	w := newWorld(t)
	w.withLinks(fakeLinks{sl: SetLinks{
		Order:    []core.RepoID{"shop-web", w.repo.ID, "shop-admin"},
		Branches: map[core.RepoID]string{"shop-web": "ygg/web", w.repo.ID: branch, "shop-admin": "ygg/admin"},
		URLs:     map[core.RepoID]string{"shop-web": webURL, "shop-admin": adminURL},
	}})
	got, _ := w.runMR("Part of a set. " + changesets.LinkMarker + " Thanks.")
	d := w.pushedDescription()
	if strings.Contains(d, changesets.LinkMarker) || !strings.Contains(d, webURL) {
		t.Fatalf("description = %q", d)
	}
	if strings.Contains(d, adminURL) || strings.Contains(d, "merge_requests/9") {
		t.Fatalf("links must point backwards only: %q", d)
	}
	if !strings.Contains(d, "shop-admin (ygg/admin)") {
		t.Fatalf("later repos are listed by name and branch: %q", d)
	}
	if got.Result == nil || !strings.Contains(got.Result.Note, webURL) {
		t.Fatalf("filled links not recorded: %+v", got.Result)
	}
}

func TestMRFirstInOrderHasNoLinksAndNeverFails(t *testing.T) {
	w := newWorld(t)
	w.withLinks(fakeLinks{sl: SetLinks{Order: []core.RepoID{w.repo.ID, "shop-web"},
		URLs: map[core.RepoID]string{"shop-web": webURL}}})
	w.runMR(changesets.LinkMarker)
	if d := w.pushedDescription(); strings.Contains(d, "merge_requests") || strings.Contains(d, changesets.LinkMarker) {
		t.Fatalf("description = %q", d)
	}
}

func TestMRLinkReadFailureAndNilLinksAreNotFatal(t *testing.T) {
	for name, l := range map[string]ChangeSetLinks{"nil": nil, "error": fakeLinks{err: errors.New("db down")}} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.withLinks(l)
			w.runMR("see " + changesets.LinkMarker)
			if d := w.pushedDescription(); strings.Contains(d, changesets.LinkMarker) || strings.Contains(d, "http") {
				t.Fatalf("description = %q", d)
			}
		})
	}
}

func TestMRWithoutMarkerIsPushedUnchangedAndConfirmedHashHolds(t *testing.T) {
	w := newWorld(t)
	w.withLinks(fakeLinks{sl: SetLinks{Order: []core.RepoID{"shop-web", w.repo.ID}, URLs: map[core.RepoID]string{"shop-web": webURL}}})
	w.runMR("plain description")
	if d := w.pushedDescription(); d != "plain description" {
		t.Fatalf("description = %q", d)
	}
	// The stored action keeps the template the user confirmed.
	w2 := newWorld(t)
	w2.withLinks(fakeLinks{sl: SetLinks{Order: []core.RepoID{"shop-web", w2.repo.ID}, URLs: map[core.RepoID]string{"shop-web": webURL}}})
	tpl := "x " + changesets.LinkMarker
	v, a := w2.runMR(tpl)
	if a.Description != tpl || strings.Contains(v.Summary, webURL) {
		t.Fatalf("template changed: %q", a.Description)
	}
}

func TestMRCreateRefusesMalformedMarker(t *testing.T) {
	w := newWorld(t)
	for name, desc := range map[string]string{
		"two markers": changesets.LinkMarker + " " + changesets.LinkMarker,
		"line break":  "a\nb",
		"too long":    strings.Repeat("x", changesets.MaxDescriptionBytes+1),
	} {
		_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.MergeRequest{
			Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", TargetBranch: "main", Title: "t",
			Description: desc, ExpectHead: w.head(w.wtDir)}})
		if err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestMRRecordedLinksAreRedacted(t *testing.T) {
	w := newWorld(t, testToken)
	w.withLinks(fakeLinks{sl: SetLinks{Order: []core.RepoID{"shop-web", w.repo.ID},
		Branches: map[core.RepoID]string{"shop-web": "ygg/" + testToken}, URLs: map[core.RepoID]string{}}})
	got, _ := w.runMR(changesets.LinkMarker)
	if got.Result == nil || strings.Contains(got.Result.Note, testToken) {
		t.Fatalf("note = %+v", got.Result)
	}
}

func TestEarlierLinksPointBackwardsOnly(t *testing.T) {
	sl := SetLinks{Order: []core.RepoID{"a", "b", "c"}, URLs: map[core.RepoID]string{"a": "ua", "b": "ub", "c": "uc"}}
	cases := map[core.RepoID][]core.RepoID{"a": nil, "b": {"a"}, "c": {"a", "b"}, "zzz": nil}
	for repo, want := range cases {
		got := earlierLinks(sl, repo)
		if len(got) != len(want) {
			t.Fatalf("%s: %v", repo, got)
		}
		for _, r := range want {
			if got[r] == "" {
				t.Fatalf("%s misses %s", repo, r)
			}
		}
	}
}

func TestCreateRefusesUntrackedRepoAndForeignWorktreeForLanding(t *testing.T) {
	w := newWorld(t)
	w.res.setUntracked(true)
	for _, a := range []coreprop.Action{
		coreprop.MergeLocal{Where: coreprop.InWorktree{ID: wtID}},
		coreprop.RemoveWorktree{Worktree: wtID},
	} {
		if _, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: a}); err == nil {
			t.Errorf("%T on an untracked repo must be refused", a)
		}
	}
	w.res.setUntracked(false)
	w.res.mu.Lock()
	w.res.wts["wt_other"] = WorktreeInfo{ID: "wt_other", Repo: "repo_other", Task: w.task.ID, Path: w.wtDir, Branch: branch}
	w.res.mu.Unlock()
	for _, a := range []coreprop.Action{
		coreprop.MergeLocal{Where: coreprop.InWorktree{ID: "wt_other"}},
		coreprop.RemoveWorktree{Worktree: "wt_other"},
	} {
		if _, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: a}); err == nil {
			t.Errorf("%T on a worktree outside the task's repos must be refused", a)
		}
	}
}
