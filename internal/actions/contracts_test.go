package actions

import (
	"context"
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The fake run of the ProposalCreator contract. The real run is
// TestProposalCreatorContractRealService in internal/proposals; both use the
// same suite, so the fake cannot drift from the live creator.
func TestProposalCreatorContractFake(t *testing.T) {
	proposalstest.ProposalCreatorContract(t, func(t *testing.T) proposalstest.RequestRig {
		e := newEnv(t)
		spec := func(req core.RequestID, i int, tag string, kind core.ProposalKind, payload []byte) ProposalSpec {
			return ProposalSpec{ID: core.ProposalID(fmt.Sprintf("prp_%s_%d", req, i)), Repo: e.repo.ID, Kind: kind, Payload: payload,
				Summary: fmt.Sprintf("%s%d", tag, i)}
		}
		kind, payload := coreproposals.RowOf(coreproposals.PullFF{Repo: e.repo.ID, Branch: "main", Upstream: "origin/main", ExpectHead: head40})
		return proposalstest.RequestRig{
			Create: func(ctx context.Context, req core.RequestID, n int, tag string) ([]core.ProposalID, error) {
				items := make([]ProposalSpec, n)
				for i := range items {
					items[i] = spec(req, i, tag, kind, payload)
				}
				return e.props.CreateForRequest(ctx, req, items)
			},
			CreateNewRepo: func(ctx context.Context, req core.RequestID) ([]core.ProposalID, error) {
				k, p := coreproposals.RowOf(coreproposals.InitRepo{Operation: "op_1", Project: e.project.ID, Repo: e.repo.ID, Leaf: "shop-new",
					Branch: "main", README: "# x\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"})
				return e.props.CreateForRequest(ctx, req, []ProposalSpec{spec(req, 0, "x", k, p)})
			},
			Find: e.props.FindForRequest,
			Pending: func(ctx context.Context, id core.ProposalID) bool {
				p, err := e.store.GetProposal(ctx, id)
				return err == nil && p.Status == core.ProposalPending && p.DecidedAt == nil
			},
			Stored: func() int {
				e.props.mu.Lock()
				defer e.props.mu.Unlock()
				n := 0
				for _, ids := range e.props.byReq {
					n += len(ids)
				}
				return n
			},
			Max: coreproposals.MaxRequestProposals,
		}
	})
}

// The fake run of the FolderDrafts contract. The real run is
// TestFolderDraftsContractRealService in internal/tasks.
func TestFolderDraftsContractFake(t *testing.T) {
	proposalstest.FolderDraftsContract(t, func(t *testing.T) proposalstest.DraftRig {
		e := newEnv(t)
		base := func() WaitingDraft {
			return WaitingDraft{Project: e.project.ID, Repo: e.repo.ID, Title: "Diagnose shop-api", Brief: "Folder unreadable.", Agent: "mock"}
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
				return e.drafts.SaveWaitingDraft(ctx, req, d)
			},
			SaveForeign: func(ctx context.Context, req core.RequestID) (core.TaskID, error) {
				d := base()
				d.Project = "prj_foreign"
				return e.drafts.SaveWaitingDraft(ctx, req, d)
			},
			Lookup:  e.tasks.LookupRequest,
			Untrack: e.scope.untrack,
			Draft: func(ctx context.Context, id core.TaskID) (core.TaskStatus, core.TaskMode, error) {
				task, err := e.store.GetTask(ctx, id)
				return task.Status, task.Mode, err
			},
			Tasks:  e.tasks.taskCount,
			Starts: e.tasks.agentCalls,
		}
	})
}
