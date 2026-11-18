package proposals

import (
	"context"
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The real run of the ProposalCreator contract: the proposals service through
// its request-bound methods, on a real SQLite store and a scripted git. The
// fake run is in internal/actions (the fake lives with its consumer).
func TestProposalCreatorContractRealService(t *testing.T) {
	proposalstest.ProposalCreatorContract(t, func(t *testing.T) proposalstest.RequestRig {
		w := newWorld(t)
		return proposalstest.RequestRig{
			Create: func(_ context.Context, req core.RequestID, n int, tag string) ([]core.ProposalID, error) {
				items := make([]RequestProposal, n)
				for i := range items {
					items[i] = w.reqItem(i, fmt.Sprintf("%s%d", tag, i))
				}
				return w.svc.CreateForRequest(w.ctx, req, items)
			},
			CreateNewRepo: func(_ context.Context, req core.RequestID) ([]core.ProposalID, error) {
				return w.svc.CreateForRequest(w.ctx, req, []RequestProposal{w.kindItem(core.ProposalInitRepo)})
			},
			Find: func(_ context.Context, req core.RequestID) ([]core.ProposalID, error) {
				return w.svc.FindForRequest(w.ctx, req)
			},
			Pending: func(_ context.Context, id core.ProposalID) bool {
				p, err := w.store.GetProposal(w.ctx, id)
				return err == nil && p.Status == core.ProposalPending && p.DecidedAt == nil
			},
			Stored: func() int { return len(w.taskProposals()) },
			Max:    coreprop.MaxRequestProposals,
		}
	})
}
