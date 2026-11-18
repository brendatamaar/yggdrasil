package tasks

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The real run of the FolderDrafts contract: tasks.Service.SaveWaitingDraft on
// a real SQLite store and the scripted adapter. The fake run is in
// internal/actions.
func TestFolderDraftsContractRealService(t *testing.T) {
	proposalstest.FolderDraftsContract(t, func(t *testing.T) proposalstest.DraftRig {
		h := newHarness(t, []string{"shop-api"})
		h.start()
		return proposalstest.DraftRig{
			Save: func(ctx context.Context, req core.RequestID, variant int) (core.TaskID, error) {
				d := waitingDraft(h)
				switch variant {
				case 1:
					d.Brief += " more"
				case 2:
					d.Title = "Other"
				case 3:
					d.Agent = "other"
				}
				return h.svc.SaveWaitingDraft(ctx, req, d)
			},
			SaveForeign: func(ctx context.Context, req core.RequestID) (core.TaskID, error) {
				d := waitingDraft(h)
				d.Project = dbtest.Project(t, h.store, core.Project{Name: "acme-admin", Path: t.TempDir()}).ID
				return h.svc.SaveWaitingDraft(ctx, req, d)
			},
			Lookup: func(ctx context.Context, req core.RequestID) (core.TaskID, error) {
				return h.svc.LookupRequest(ctx, req)
			},
			Untrack: func() { dbtest.UntrackRepo(t, h.store, h.repos[0]) },
			Draft: func(ctx context.Context, id core.TaskID) (core.TaskStatus, core.TaskMode, error) {
				task, err := h.store.GetTask(ctx, id)
				return task.Status, task.Mode, err
			},
			Tasks: func() int {
				list, err := h.store.ListTasks(context.Background(), h.project.ID)
				if err != nil {
					t.Fatal(err)
				}
				return len(list)
			},
			Starts: func() int { return len(h.adapter.Starts()) },
		}
	})
}
