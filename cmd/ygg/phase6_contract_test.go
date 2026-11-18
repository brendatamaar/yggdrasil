package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The real adapters of the composition root run the same contract suites as
// the fakes in internal/actions: the ProposalCreator over the real proposals
// service and the real task service, and FolderDrafts over SaveWaitingDraft.

func TestProposalCreatorContractRealAdapter(t *testing.T) {
	proposalstest.ProposalCreatorContract(t, func(t *testing.T) proposalstest.RequestRig {
		r := newP6Rig(t)
		p := r.newRepo(t)
		r.refresh(t, p)
		c := &proposalCreator{svc: r.a.proposals, drafts: r.a.tasks, store: r.a.store}
		ctx := context.Background()
		spec := func(i int, tag string) actions.ProposalSpec {
			action, err := ignoreAction(context.Background(), p.id, p.dir, []string{fmt.Sprintf("/%s%d/node_modules/", tag, i)})
			if err != nil {
				t.Fatal(err)
			}
			kind, payload := coreprop.RowOf(action)
			return actions.ProposalSpec{Repo: p.id, Kind: kind, Payload: payload, Summary: fmt.Sprintf("ignore %s %d", tag, i)}
		}
		return proposalstest.RequestRig{
			Create: func(ctx context.Context, req core.RequestID, n int, tag string) ([]core.ProposalID, error) {
				items := make([]actions.ProposalSpec, n)
				for i := range items {
					items[i] = spec(i, tag)
				}
				return c.CreateForRequest(ctx, req, items)
			},
			CreateNewRepo: func(ctx context.Context, req core.RequestID) ([]core.ProposalID, error) {
				kind, payload := coreprop.RowOf(coreprop.InitRepo{Operation: "op_1", Project: p.project, Repo: p.id, Leaf: "shop-new",
					Branch: "main", README: "# x\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"})
				return c.CreateForRequest(ctx, req, []actions.ProposalSpec{{Repo: p.id, Kind: kind, Payload: payload, Summary: "new repo"}})
			},
			Find: c.FindForRequest,
			Pending: func(ctx context.Context, id core.ProposalID) bool {
				v, err := r.a.store.GetProposal(ctx, id)
				return err == nil && v.Status == core.ProposalPending && v.DecidedAt == nil
			},
			Stored: func() int {
				views, err := r.a.proposals.List(ctx, proposals.ListFilter{Status: core.ProposalPending})
				if err != nil {
					t.Fatal(err)
				}
				return len(views)
			},
			Max: coreprop.MaxRequestProposals,
		}
	})
}

func TestFolderDraftsContractRealAdapter(t *testing.T) {
	proposalstest.FolderDraftsContract(t, func(t *testing.T) proposalstest.DraftRig {
		r := newP6Rig(t)
		p := r.newRepo(t)
		r.refresh(t, p)
		f := folderDrafts{svc: r.a.tasks}
		ctx := context.Background()
		base := func() actions.WaitingDraft {
			return actions.WaitingDraft{Project: p.project, Repo: p.id, Title: "Diagnose shop-api", Brief: "Folder unreadable.", Agent: "mock"}
		}
		repo, err := r.a.store.GetRepo(ctx, p.id)
		if err != nil {
			t.Fatal(err)
		}
		return proposalstest.DraftRig{
			Save: func(ctx context.Context, req core.RequestID, variant int) (core.TaskID, error) {
				d := base()
				switch variant {
				case 1:
					d.Brief += " more"
				case 2:
					d.Title = "Other"
				case 3:
					d.Agent = "other"
				}
				return f.SaveWaitingDraft(ctx, req, d)
			},
			SaveForeign: func(ctx context.Context, req core.RequestID) (core.TaskID, error) {
				d := base()
				d.Project = dbtest.Project(t, r.a.store, core.Project{Name: "acme-admin", Path: t.TempDir()}).ID
				return f.SaveWaitingDraft(ctx, req, d)
			},
			Lookup:  r.a.tasks.LookupRequest,
			Untrack: func() { dbtest.UntrackRepo(t, r.a.store, repo) },
			Draft: func(ctx context.Context, id core.TaskID) (core.TaskStatus, core.TaskMode, error) {
				task, err := r.a.store.GetTask(ctx, id)
				return task.Status, task.Mode, err
			},
			Tasks: func() int {
				list, err := r.a.store.ListTasks(ctx, p.project)
				if err != nil {
					t.Fatal(err)
				}
				return len(list)
			},
			// A draft that never started has no turn: count tasks that left draft.
			Starts: func() int {
				list, err := r.a.store.ListTasks(ctx, p.project)
				if err != nil {
					t.Fatal(err)
				}
				n := 0
				for _, task := range list {
					if task.Status != core.TaskDraft {
						n++
					}
				}
				return n
			},
		}
	})
}
