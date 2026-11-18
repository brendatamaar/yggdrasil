package proposals

import (
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// MCP-05, NR-04: an agent may create a PublishRepo proposal, which stays pending
// until the user confirms its exact destination, and may never create an
// InitRepo proposal: initialization starts only from the user's dialog
// (docs/AGENTS.md section 6, invariant 2).
func TestAgentCanCreateAPublishButNeverAnInit(t *testing.T) {
	x := newNR(t)
	agent := coreprop.AgentSource{Task: nrSetup}

	// Never an InitRepo: refused, nothing stored, the reservation untouched.
	_, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: x.initAction(), Source: agent})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent proposed an InitRepo: %v", err)
	}
	if n, _ := x.svc.PendingCount(x.ctx, nrSetup); n != 0 {
		t.Fatalf("%d proposals stored after a refused agent InitRepo", n)
	}
	rec, err := x.store.GetReservation(x.ctx, nrOp)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Operation.State.(newrepos.Reserved); !ok || rec.Proposal != "" {
		t.Fatalf("a refused agent InitRepo changed the reservation: %+v", rec)
	}

	// A PublishRepo of a repo that is not ready is refused for an agent too.
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: x.publishAction(), Source: agent}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent proposed a publish before the repo was initialized: %v", err)
	}

	x.toReady()
	v, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: x.publishAction(), Source: agent, Summary: "ship it"})
	if err != nil {
		t.Fatalf("an agent could not propose a publish: %v", err)
	}
	if v.Status != core.ProposalPending || !v.FromAgent {
		t.Fatalf("view = %+v", v)
	}
	// Creating it executes nothing: the lifecycle is not called and no remote is made.
	if x.fake.Confirms() != 0 {
		t.Fatal("an agent-created publish called the lifecycle")
	}
	if p, err := x.svc.Get(x.ctx, v.ID); err != nil || p.Status != core.ProposalPending {
		t.Fatalf("get: %+v %v", p, err)
	}
}

// An agent-created publish is bound to the task's own tracked repo.
func TestAgentPublishIsBoundToTheTasksRepo(t *testing.T) {
	x := newNR(t)
	x.toReady()
	// Another task does not have this repo in scope.
	_, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: x.publishAction(), Source: coreprop.AgentSource{Task: x.task.ID}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent of another task proposed the publish: %v", err)
	}
	// A different operation, or one that names another repo, is refused.
	other := x.publishAction()
	other.Operation = "op_other"
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: other, Source: coreprop.AgentSource{Task: nrSetup}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent proposed the publish of an unknown operation: %v", err)
	}
	wrong := x.publishAction()
	wrong.Repo = "rep_other"
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: wrong, Source: coreprop.AgentSource{Task: nrSetup}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent proposed a publish naming another repo than the operation's: %v", err)
	}
	if n, _ := x.svc.PendingCount(x.ctx, nrSetup); n != 0 {
		t.Fatalf("%d proposals stored", n)
	}
}
