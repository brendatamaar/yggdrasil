package proposals

import (
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// kindItem is a request item of a new-repo kind. Both carry a valid payload, so
// the refusal comes from the kind and not from a bad payload.
func (w *world) kindItem(kind core.ProposalKind) RequestProposal {
	w.t.Helper()
	var a coreprop.Action
	switch kind {
	case core.ProposalInitRepo:
		a = coreprop.InitRepo{Operation: "op_1", Project: "prj_1", Repo: w.repo.ID, Leaf: "shop-new", Branch: "main",
			README: "# shop-new\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
	case core.ProposalPublish:
		a = coreprop.PublishRepo{Operation: "op_1", Repo: w.repo.ID, HostInstance: "git.acme.dev", Owner: "acme", OwnerKind: "org",
			Name: "shop-new", Visibility: "private", Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: strings.Repeat("c", 16)}
	default:
		w.t.Fatalf("not a new-repo kind: %s", kind)
	}
	if err := a.Validate(); err != nil {
		w.t.Fatalf("the %s fixture is not valid on its own: %v", kind, err)
	}
	k, payload := coreprop.RowOf(a)
	return RequestProposal{Task: w.task.ID, Repo: w.repo.ID, Summary: "new repo", Kind: k, Payload: payload}
}

func (w *world) auditRowCount() int {
	w.t.Helper()
	rows, err := w.store.ListAudit(w.ctx, "", 5000)
	if err != nil {
		w.t.Fatal(err)
	}
	return len(rows)
}

// P3: InitRepo and PublishRepo start from the new repo dialog (NR-01), never
// from a workspace action. A request carrying one, alone or next to good items,
// is refused as unsupported, stores nothing and audits nothing; the good items
// alone then succeed under the same request ID.
func TestCreateForRequestRefusesNewRepoKinds(t *testing.T) {
	for _, kind := range []core.ProposalKind{core.ProposalInitRepo, core.ProposalPublish} {
		t.Run(string(kind), func(t *testing.T) {
			if !coreprop.Enabled(kind) {
				t.Fatal("the kind must be enabled for this test to reach the kind rule")
			}
			w := newWorld(t)
			good := w.reqItem(0, "ok")
			bad := w.kindItem(kind)
			before := w.auditRowCount()
			for name, items := range map[string][]RequestProposal{
				"alone":       {bad},
				"after good":  {good, bad},
				"before good": {bad, good},
			} {
				req := core.RequestID("req_kind_" + strings.ReplaceAll(name, " ", "_"))
				ids, err := w.svc.CreateForRequest(w.ctx, req, items)
				var r *Refusal
				if !errors.Is(err, ErrRefused) || !errors.As(err, &r) || r.Reason != coreprop.RefuseUnsupported || len(ids) != 0 ||
					!strings.Contains(r.UserMessage(), "new repo dialog") {
					t.Fatalf("%s: ids %v, err %v: want an unsupported refusal", name, ids, err)
				}
				if found := w.find(req); len(found) != 0 {
					t.Fatalf("%s: FindForRequest = %v after a refusal", name, found)
				}
			}
			if got := w.taskProposals(); len(got) != 0 {
				t.Fatalf("%d proposals stored by refused requests", len(got))
			}
			if n := w.auditRowCount(); n != before {
				t.Fatalf("%d audit rows written by refused requests", n-before)
			}
			// The request id is not poisoned: the good item alone goes through.
			ids := w.createFor("req_kind_alone", []RequestProposal{good})
			if len(ids) != 1 || len(w.taskProposals()) != 1 {
				t.Fatalf("retry after refusal: ids %v, stored %d", ids, len(w.taskProposals()))
			}
		})
	}
}

// The core plan is only reached with items the shell accepted: it does not
// itself know the new-repo kinds are off limits for requests, so the shell
// check above is the single guard. This pins that division of labour; if the
// core starts refusing them too, update both tests together.
func TestPlanRequestLeavesNewRepoKindsToTheShell(t *testing.T) {
	a := coreprop.InitRepo{Operation: "op_1", Project: "prj_1", Repo: "rep_1", Leaf: "shop-new", Branch: "main", README: "# x\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
	plan := coreprop.PlanRequest("req_1", []coreprop.RequestItem{{Task: "tsk_1", Repo: "rep_1", Summary: "s", Action: a}}, nil)
	if _, ok := plan.(coreprop.CreateAll); !ok {
		t.Fatalf("plan = %#v", plan)
	}
}
