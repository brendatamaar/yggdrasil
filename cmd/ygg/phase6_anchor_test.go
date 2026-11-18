package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// renamedRepoStore answers GetRepo with a different name each call, as if the
// user renamed the repo between two actions.
type renamedRepoStore struct {
	inner *db.Store
	calls int
}

func (s *renamedRepoStore) GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error) {
	r, err := s.inner.GetRepo(ctx, id)
	s.calls++
	r.Name = fmt.Sprintf("%s-renamed-%d", r.Name, s.calls)
	return r, err
}

func anchorSpec(t *testing.T, p *p6Repo, tag string) actions.ProposalSpec {
	t.Helper()
	action, err := ignoreAction(context.Background(), p.id, p.dir, []string{"/" + tag + "/node_modules/"})
	if err != nil {
		t.Fatal(err)
	}
	kind, payload := coreprop.RowOf(action)
	return actions.ProposalSpec{Repo: p.id, Kind: kind, Payload: payload, Summary: "ignore " + tag}
}

// A repo rename between two requests returns the same anchor (replay, no
// digest mismatch); the anchor is the non-startable kind with a fixed title.
func TestAnchorSurvivesRepoRename(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	ctx := context.Background()
	store := &renamedRepoStore{inner: r.a.store}
	c := &proposalCreator{svc: r.a.proposals, drafts: r.a.tasks, store: store}

	first, err := c.anchor(ctx, p.id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.anchor(ctx, p.id)
	if err != nil {
		t.Fatalf("anchor after rename: %v", err)
	}
	if first != second {
		t.Fatalf("rename made another anchor: %s then %s", first, second)
	}
	task, err := r.a.store.GetTask(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !coretasks.IsAnchor(task) || task.Title != coretasks.AnchorTitle || task.Status != core.TaskDraft {
		t.Fatalf("anchor task: %+v", task)
	}
	if _, err := r.a.tasks.StartDraft(ctx, coretasks.DraftStart{Task: first, Mode: core.ModeAsk}, coretasks.AnchorAgent); err == nil {
		t.Fatal("the anchor draft started")
	}
}

// Proposals of a repo are never refused because of an ID the user chose: many
// requests with user-chosen IDs (even look-alikes) share the one anchor.
func TestAnchorProposalsNeverRefusedByUserIDs(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	ctx := context.Background()
	c := &proposalCreator{svc: r.a.proposals, drafts: r.a.tasks, store: r.a.store}
	for i, id := range []core.RequestID{"req_a", "req_b", "anchor", "anchor_", "req-anchor-" + core.RequestID(p.id)} {
		if _, err := c.CreateForRequest(ctx, id, []actions.ProposalSpec{anchorSpec(t, p, fmt.Sprintf("t%d", i))}); err != nil {
			t.Fatalf("request %q: %v", id, err)
		}
	}
	all, err := r.a.store.ListTasks(ctx, p.project)
	if err != nil {
		t.Fatal(err)
	}
	anchors := 0
	for _, task := range all {
		if coretasks.IsAnchor(task) {
			anchors++
		}
	}
	if anchors != 1 {
		t.Fatalf("anchors for the repo = %d, want 1", anchors)
	}
}
