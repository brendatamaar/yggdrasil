package proposals

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

const (
	nrOp     = core.OperationID("op_shop")
	nrRepo   = core.RepoID("rep_shopnew")
	nrSetup  = core.TaskID("tsk_setup")
	nrMarker = "0123456789abcdef"
)

var nrHead = strings.Repeat("c", 40)

// nrW is a world with a reserved new repo (real database rows: the reserved
// repo, its setup draft and the reservation) and the fake lifecycle, whose
// every state change is mirrored into the reservation row and reported to the
// service the way the real lifecycle's hub wiring will.
type nrW struct {
	*world
	fake *proposalstest.NewRepoFake
	spec newrepos.InitSpec
	pub  newrepos.PublishSpec
}

func newNR(t *testing.T, secrets ...string) *nrW {
	t.Helper()
	w := newWorld(t, secrets...)
	w.cfg.Resolver = StoreResolver{Store: w.store}
	fake := proposalstest.NewNewRepoFake()
	w.cfg.NewRepos = fake
	w.svc.Close()
	w.start()
	proj, err := w.store.GetProject(w.ctx, w.repo.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	x := &nrW{world: w, fake: fake}
	x.spec = newrepos.InitSpec{Project: proj.ID, Repo: nrRepo, Leaf: "shop-new", Branch: "main",
		README: "# shop-new\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
	x.pub = newrepos.PublishSpec{Repo: nrRepo, HostInstance: "github.com", Owner: "acme", OwnerKind: newrepos.OwnerOrg, Name: "shop-new",
		Visibility: newrepos.VisibilityPrivate, Branch: "main", ExpectedHead: nrHead, Marker: nrMarker}
	if err := w.store.Commit(w.ctx, func(tx *db.Tx) error {
		return tx.ReserveRepo(w.ctx, db.ReserveRepo{Operation: nrOp, Project: proj.ID, Repo: nrRepo, SetupTask: nrSetup,
			Path: filepath.Join(proj.Path, "shop-new"), Spec: x.spec, Agent: "mock", Title: "Set up shop-new", Now: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	fake.AddInit(nrOp, x.spec.Hash())
	fake.OnChange(x.mirror)
	return x
}

func (x *nrW) initAction() coreprop.InitRepo {
	s := x.spec
	return coreprop.InitRepo{Operation: nrOp, Project: s.Project, Repo: s.Repo, Leaf: s.Leaf, Branch: s.Branch,
		README: s.README, Ignore: s.Ignore, CommitMessage: s.CommitMessage}
}

func (x *nrW) publishAction() coreprop.PublishRepo {
	p := x.pub
	return coreprop.PublishRepo{Operation: nrOp, Repo: p.Repo, HostInstance: p.HostInstance, Owner: p.Owner, OwnerKind: string(p.OwnerKind),
		Name: p.Name, Visibility: string(p.Visibility), Branch: p.Branch, ExpectedHead: p.ExpectedHead, Marker: p.Marker}
}

// createNR creates a proposal on the setup task as the user.
func (x *nrW) createNR(a coreprop.Action) (View, error) {
	return x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: a, Summary: "new repo", Source: coreprop.UserSource{}})
}

func (x *nrW) mustCreate(a coreprop.Action) View {
	x.t.Helper()
	v, err := x.createNR(a)
	if err != nil {
		x.t.Fatalf("create %T: %v", a, err)
	}
	return v
}

// stateFor is the sealed lifecycle state of a fake operation state.
func (x *nrW) stateFor(s proposalstest.OpState, proposal core.ProposalID) newrepos.State {
	if s.Flow == proposalstest.FlowPublish {
		resume := newrepos.ResumePublish{Spec: x.pub}
		switch s.Phase {
		case proposalstest.PhaseRunning:
			return newrepos.Publishing{Spec: x.pub, Attempt: s.Attempt, Stage: newrepos.StageCreateRemote}
		case proposalstest.PhaseUncertain:
			return newrepos.Uncertain{Attempt: s.Attempt, Stage: newrepos.StageCreateRemote, Reason: "outcome_unknown", Resume: resume}
		case proposalstest.PhaseStopped:
			return newrepos.Stopped{Stage: newrepos.StageCreateRemote, Reason: newrepos.StopReason(s.Reason), Resume: resume, Attempt: s.Attempt}
		case proposalstest.PhasePublished:
			return newrepos.Published{RemoteID: "4711", URL: "https://github.com/acme/shop-new", Head: x.pub.ExpectedHead}
		}
		return newrepos.Ready{Repo: nrRepo, InitialHead: nrHead}
	}
	resume := newrepos.ResumeInit{Spec: x.spec}
	switch s.Phase {
	case proposalstest.PhaseRunning:
		return newrepos.Initializing{Spec: x.spec, Attempt: s.Attempt, Stage: newrepos.StageCreateDirectory}
	case proposalstest.PhaseUncertain:
		return newrepos.Uncertain{Attempt: s.Attempt, Stage: newrepos.StageCreateDirectory, Reason: "outcome_unknown", Resume: resume}
	case proposalstest.PhaseStopped:
		return newrepos.Stopped{Stage: newrepos.StageCreateDirectory, Reason: newrepos.StopReason(s.Reason), Resume: resume, Attempt: s.Attempt}
	case proposalstest.PhaseReady, proposalstest.PhasePublished:
		return newrepos.Ready{Repo: nrRepo, InitialHead: nrHead}
	}
	if proposal != "" {
		return newrepos.AwaitingConfirm{Spec: x.spec, Proposal: proposal, Hash: x.spec.Hash()}
	}
	return newrepos.Reserved{Spec: x.spec, Hash: x.spec.Hash()}
}

// saveState writes the operation's state, as the lifecycle would.
func (x *nrW) saveState(st newrepos.State) {
	x.t.Helper()
	rec, err := x.store.GetReservation(x.ctx, nrOp)
	if err != nil {
		x.t.Error(err)
		return
	}
	if err := x.store.Commit(x.ctx, func(tx *db.Tx) error {
		if _, ok := st.(newrepos.Ready); ok {
			if err := tx.MarkRepoReady(x.ctx, nrRepo, "main"); err != nil && !errors.Is(err, db.ErrNotFound) {
				return err
			}
		}
		_, err := tx.SaveReservation(x.ctx, nrOp, st, rec.Proposal, rec.Operation.Version, time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC))
		return err
	}); err != nil {
		x.t.Error(err)
	}
}

// mirror is the fake's OnChange: persist the state, then tell the service.
func (x *nrW) mirror(c proposalstest.Change) {
	rec, err := x.store.GetReservation(x.ctx, c.Op)
	if err != nil {
		x.t.Error(err)
		return
	}
	x.saveState(x.stateFor(c.State, rec.Proposal))
	_ = x.svc.OperationChanged(x.ctx, c.Op)
}

// toReady brings the operation to Ready (initialized and tracked) without a
// proposal, and makes the fake accept the publish spec.
func (x *nrW) toReady() {
	x.t.Helper()
	x.saveState(newrepos.Ready{Repo: nrRepo, InitialHead: nrHead})
	x.fake.AddReady(nrOp, x.pub.Hash())
}

func (x *nrW) restart() {
	x.svc.Close()
	x.start()
}

func (x *nrW) phase() proposalstest.OpPhase {
	s, _ := x.fake.State(nrOp)
	return s.Phase
}

// waitConfirmedAndRunning waits until the lifecycle was asked and the
// proposal is still Confirmed (the work is the lifecycle's to finish).
func (x *nrW) waitConfirmedAndRunning(id core.ProposalID) {
	x.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for x.fake.Confirms() == 0 || x.phase() != proposalstest.PhaseRunning {
		if time.Now().After(deadline) {
			x.t.Fatalf("the lifecycle was not started (confirms %d, phase %s)", x.fake.Confirms(), x.phase())
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Give the executor time to finish its hand-off, then check nothing recorded a result.
	x.settleQueue()
	if v, err := x.svc.Get(x.ctx, id); err != nil || v.Status != core.ProposalConfirmed {
		x.t.Fatalf("proposal = %+v, %v: want it to stay confirmed while the lifecycle works", v, err)
	}
}

// settleQueue waits for the service's executor queues to drain.
func (x *nrW) settleQueue() {
	x.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		x.svc.qmu.Lock()
		busy := len(x.svc.running) > 0
		x.svc.qmu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			x.t.Fatal("the executor queue did not drain")
		}
		time.Sleep(time.Millisecond)
	}
}

func (x *nrW) effectStates(id core.ProposalID) []db.EffectState {
	x.t.Helper()
	es, err := x.store.ListEffectsByProposal(x.ctx, id)
	if err != nil {
		x.t.Fatal(err)
	}
	var out []db.EffectState
	for _, e := range es {
		out = append(out, e.State)
	}
	return out
}
