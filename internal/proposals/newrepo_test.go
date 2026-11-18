package proposals

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// ---- creation: the narrow init resolver ----

func TestInitRepoCreateIsScopedToItsReservedOperation(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	if v.Status != core.ProposalPending || v.RepoName != "shop-new" || v.Task != nrSetup {
		t.Fatalf("view = %+v", v)
	}
	rec, err := x.store.GetReservation(x.ctx, nrOp)
	if err != nil {
		t.Fatal(err)
	}
	aw, ok := rec.Operation.State.(newrepos.AwaitingConfirm)
	if !ok || aw.Proposal != v.ID || rec.Proposal != v.ID {
		t.Fatalf("the operation is not linked to the proposal: %+v", rec)
	}
	if x.fake.Confirms() != 0 {
		t.Fatal("creating the proposal called the lifecycle")
	}
	if got := x.auditActions(v.ID); !strings.Contains(strings.Join(got, ","), "proposal.create") || !strings.Contains(strings.Join(got, ","), "proposal.effect") {
		t.Fatalf("audit = %v", got)
	}
	// One proposal per reserved operation.
	if _, err := x.createNR(x.initAction()); !errors.Is(err, ErrRefused) {
		t.Fatalf("a second proposal for the same operation: %v", err)
	}
}

func TestInitRepoCreateRefusals(t *testing.T) {
	cases := map[string]func(x *nrW) CreateRequest{
		"another task": func(x *nrW) CreateRequest {
			return CreateRequest{Task: x.task.ID, Action: x.initAction(), Source: coreprop.UserSource{}}
		},
		"an agent": func(x *nrW) CreateRequest {
			return CreateRequest{Task: nrSetup, Action: x.initAction(), Source: coreprop.AgentSource{Task: nrSetup}}
		},
		"a different readme": func(x *nrW) CreateRequest {
			a := x.initAction()
			a.README = "# something else\n"
			return CreateRequest{Task: nrSetup, Action: a, Source: coreprop.UserSource{}}
		},
		"a different leaf": func(x *nrW) CreateRequest {
			a := x.initAction()
			a.Leaf = "other"
			return CreateRequest{Task: nrSetup, Action: a, Source: coreprop.UserSource{}}
		},
		"an unknown operation": func(x *nrW) CreateRequest {
			a := x.initAction()
			a.Operation = "op_nope"
			return CreateRequest{Task: nrSetup, Action: a, Source: coreprop.UserSource{}}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			x := newNR(t)
			if _, err := x.svc.Create(x.ctx, mk(x)); !errors.Is(err, ErrRefused) {
				t.Fatalf("Create = %v, want a refusal", err)
			}
			rec, _ := x.store.GetReservation(x.ctx, nrOp)
			if _, ok := rec.Operation.State.(newrepos.Reserved); !ok || rec.Proposal != "" {
				t.Fatalf("a refused creation changed the operation: %+v", rec)
			}
		})
	}
}

// Ordinary proposals still need a ready repo: the reserved repo is invisible to
// every other kind, so only the init resolver reaches it.
func TestReservedRepoIsInvisibleToOrdinaryProposals(t *testing.T) {
	x := newNR(t)
	for _, a := range []coreprop.Action{
		coreprop.IgnoreEntries{Where: coreprop.MainCheckout{Repo: nrRepo}, Entries: []string{"/dist/"}, ExpectedAbsent: true},
		coreprop.PullFF{Repo: nrRepo, Branch: "main", Upstream: "origin/main", ExpectHead: nrHead},
		coreprop.PublishRepo{Operation: nrOp, Repo: nrRepo, HostInstance: "github.com", Owner: "acme", OwnerKind: "org", Name: "shop-new",
			Visibility: "private", Branch: "main", ExpectedHead: nrHead, Marker: nrMarker},
	} {
		if _, err := x.createNR(a); !errors.Is(err, ErrRefused) {
			t.Errorf("Create(%s) on a reserved repo = %v, want a refusal", a.Kind(), err)
		}
	}
	if n, _ := x.svc.PendingCount(x.ctx, nrSetup); n != 0 {
		t.Errorf("%d proposals were stored for a reserved repo", n)
	}
}

func TestPublishCreateNeedsAReadyOperationAndRefusesAgents(t *testing.T) {
	x := newNR(t)
	if _, err := x.createNR(x.publishAction()); !errors.Is(err, ErrRefused) {
		t.Fatalf("publish before the repo is initialized: %v", err)
	}
	x.toReady()
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: nrSetup, Action: x.publishAction(), Source: coreprop.AgentSource{Task: nrSetup}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an agent proposed a publish: %v", err)
	}
	v := x.mustCreate(x.publishAction())
	if v.Status != core.ProposalPending || x.fake.Confirms() != 0 {
		t.Fatalf("view = %+v, confirms %d", v, x.fake.Confirms())
	}
	other := x.publishAction()
	other.Operation = "op_other"
	if _, err := x.createNR(other); !errors.Is(err, ErrRefused) {
		t.Fatalf("publish of an unknown operation: %v", err)
	}
}

// ---- the confirmation text ----

func TestPublishConfirmationShowsDestinationVisibilityBranchAndHead(t *testing.T) {
	x := newNR(t)
	x.toReady()
	v := x.mustCreate(x.publishAction())
	text := strings.Join(v.Lines, "\n")
	for _, want := range []string{"acme/shop-new", "github.com", "private", "main", nrHead, nrMarker, "never forced"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "PUBLIC") {
		t.Errorf("a private publish warns about public:\n%s", text)
	}
	pub := x.publishAction()
	pub.Visibility = "public"
	pubText := strings.Join(joinLines(coreprop.Describe(pub)), "\n")
	if !strings.Contains(pubText, "PUBLIC") || !strings.Contains(pubText, "public repository") {
		t.Errorf("a public publish is not marked:\n%s", pubText)
	}
	// Private is the explicit default: an action that does not choose is invalid, never public.
	none := x.publishAction()
	none.Visibility = ""
	if none.Validate() == nil {
		t.Error("an action with no visibility is valid")
	}
}

func joinLines(ls []coreprop.Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Text)
	}
	return out
}

// ---- executor: only after a matching confirmation, work owned by the lifecycle ----

func TestPropInitOnlyAfterConfirm(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	if x.fake.Confirms() != 0 {
		t.Fatal("the lifecycle was called before any confirmation")
	}
	// A wrong hash changes nothing and calls nothing.
	if _, err := x.svc.Confirm(x.ctx, v.ID, strings.Repeat("0", 64), "user"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Confirm with a wrong hash = %v", err)
	}
	x.settleQueue()
	if x.fake.Confirms() != 0 || x.fake.Reconciles() != 0 {
		t.Fatal("a refused confirmation reached the lifecycle")
	}
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	calls := x.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "ConfirmInit" || calls[0].Op != nrOp || calls[0].Hash != x.spec.Hash() || calls[0].Attempt != 1 {
		t.Fatalf("calls = %+v, want one ConfirmInit with the spec hash", calls)
	}
	// The hand-off is not the result: the proposal follows the operation.
	x.fake.Finish(nrOp)
	got := x.waitFor(v.ID, core.ProposalExecuted)
	if got.Result == nil || got.Result.CommitID != nrHead {
		t.Fatalf("result = %+v", got.Result)
	}
	if es := x.effectStates(v.ID); len(es) != 1 {
		t.Fatalf("effects = %v", es)
	}
	if x.fake.Confirms() != 1 {
		t.Fatalf("confirms = %d", x.fake.Confirms())
	}
	// The repo is ordinary now.
	if r, err := x.store.GetRepo(x.ctx, nrRepo); err != nil || r.Name != "shop-new" {
		t.Fatalf("repo = %+v, %v", r, err)
	}
}

func TestPublishFollowsTheOperationToPublished(t *testing.T) {
	x := newNR(t)
	x.toReady()
	v := x.mustCreate(x.publishAction())
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	calls := x.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "ConfirmPublish" || calls[0].Hash != x.pub.Hash() {
		t.Fatalf("calls = %+v", calls)
	}
	x.fake.Finish(nrOp)
	got := x.waitFor(v.ID, core.ProposalExecuted)
	if got.Result == nil || got.Result.CommitID != nrHead || !strings.Contains(got.Result.Note, "github.com/acme/shop-new") {
		t.Fatalf("result = %+v", got.Result)
	}
}

func TestStoppedOperationFailsTheProposalAndRetryNeedsANewAttempt(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	x.fake.Stop(nrOp, "stage_failed")
	got := x.waitFor(v.ID, core.ProposalFailed)
	if !got.Retryable || got.Attempt != 1 || !strings.Contains(got.Message, "Nothing was deleted") {
		t.Fatalf("failure = %+v", got)
	}
	// A human retry is a fresh confirmation: it reaches the lifecycle's Retry
	// with the immutable hash, never a second confirm (a stopped operation
	// ignores a confirm, and the executor would have looped in reconcile).
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	for i := 0; x.fake.Retries() < 1; i++ {
		if i > 5000 {
			t.Fatal("the retry never reached the lifecycle")
		}
		time.Sleep(2 * time.Millisecond)
	}
	x.settleQueue()
	calls := x.fake.Calls()
	if len(calls) != 2 || calls[0].Method != "ConfirmInit" || calls[1].Method != "Retry" || calls[1].Hash != x.spec.Hash() {
		t.Fatalf("calls = %+v, want one confirm then one retry with the spec hash", calls)
	}
	if s, _ := x.fake.State(nrOp); s.Attempt <= calls[0].Attempt {
		t.Fatalf("the retry attempt %d is not newer than %d", s.Attempt, calls[0].Attempt)
	}
	x.fake.Finish(nrOp)
	x.waitFor(v.ID, core.ProposalExecuted)
}

// The publish flow retries through the same port call.
func TestStoppedPublishIsRetriedThroughRetry(t *testing.T) {
	x := newNR(t)
	x.toReady()
	v := x.mustCreate(x.publishAction())
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	x.fake.Stop(nrOp, "host_refused")
	x.waitFor(v.ID, core.ProposalFailed)
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	for i := 0; x.fake.Retries() < 1; i++ {
		if i > 5000 {
			t.Fatal("the retry never reached the lifecycle")
		}
		time.Sleep(2 * time.Millisecond)
	}
	x.settleQueue()
	if x.fake.Confirms() != 1 || x.fake.Reconciles() != 0 {
		t.Fatalf("confirms %d, reconciles %d: a retry must not confirm or reconcile", x.fake.Confirms(), x.fake.Reconciles())
	}
	x.fake.Finish(nrOp)
	x.waitFor(v.ID, core.ProposalExecuted)
}

// A retry the lifecycle refuses (nothing written) is a definite refusal: the
// hand-off is failed, the proposal fails and can be retried, and nothing loops
// through reconciliation.
func TestRefusedRetryIsADefiniteRefusalNotAReconcileLoop(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	x.fake.Stop(nrOp, "stage_failed")
	x.waitFor(v.ID, core.ProposalFailed)
	x.fake.Script(proposalstest.FailBefore, proposalstest.ErrFakeRefused{Msg: "Something changed since you confirmed."})
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	x.waitForAttempt(v.ID, 2)
	got := x.waitFor(v.ID, core.ProposalFailed)
	if !got.Retryable || got.Attempt != 2 || !strings.Contains(got.Message, "Something changed") {
		t.Fatalf("after a refused retry: %+v", got)
	}
	if x.fake.Retries() != 1 || x.fake.Reconciles() != 0 {
		t.Fatalf("retries %d, reconciles %d", x.fake.Retries(), x.fake.Reconciles())
	}
	if es := x.effectStates(v.ID); len(es) != 2 || es[1] != db.EffectFailed {
		t.Fatalf("effects = %v, want the refused hand-off failed", es)
	}
}

// A rejection or an untracked project never resumes: the proposal fails and a
// retry is refused before the lifecycle is called.
func TestNonResumableStopIsNotRetried(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	x.confirm(v)
	x.waitConfirmedAndRunning(v.ID)
	x.fake.Stop(nrOp, "conflict")
	got := x.waitFor(v.ID, core.ProposalFailed)
	if got.Retryable {
		t.Fatalf("a conflict is retryable: %+v", got)
	}
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("Retry of a terminal failure = %v", err)
	}
	if x.fake.Confirms() != 1 {
		t.Fatalf("confirms = %d", x.fake.Confirms())
	}
}

func TestPropStaleHashRefusedNewRepo(t *testing.T) {
	t.Run("init: the proposal differs from the reserved spec", func(t *testing.T) {
		x := newNR(t)
		// A proposal whose action differs from the reserved spec, linked to the operation.
		a := x.initAction()
		a.README = "# changed after review\n"
		v := View{ID: "prp_tampered"}
		x.insertProposal(v.ID, a)
		rec, _ := x.store.GetReservation(x.ctx, nrOp)
		x.relink(rec, v.ID)
		x.confirm(v2(x, v.ID))
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailStale) || got.Retryable {
			t.Fatalf("failure = %+v", got)
		}
		if x.fake.Confirms() != 0 || x.fake.Reconciles() != 0 {
			t.Fatal("a stale proposal reached the lifecycle")
		}
		if es := x.effectStates(v.ID); len(es) != 0 {
			t.Fatalf("a hand-off was recorded for a stale proposal: %v", es)
		}
	})
	t.Run("init: the proposal is not the one the operation waits for", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.insertProposal("prp_other", x.initAction())
		rec, _ := x.store.GetReservation(x.ctx, nrOp)
		x.relink(rec, "prp_other")
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailStale) || x.fake.Confirms() != 0 {
			t.Fatalf("failure = %+v, confirms %d", got, x.fake.Confirms())
		}
	})
	t.Run("publish: another publish is already in progress", func(t *testing.T) {
		x := newNR(t)
		x.toReady()
		v := x.mustCreate(x.publishAction())
		other := x.pub
		other.Name = "shop-other"
		x.saveState(newrepos.Publishing{Spec: other, Attempt: 1, Stage: newrepos.StageCreateRemote})
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailStale) || x.fake.Confirms() != 0 {
			t.Fatalf("failure = %+v, confirms %d", got, x.fake.Confirms())
		}
	})
	t.Run("a hash the lifecycle does not know is refused and recorded", func(t *testing.T) {
		x := newNR(t)
		x.fake.AddInit(nrOp, "some-other-hash") // the lifecycle holds another immutable spec
		v := x.mustCreate(x.initAction())
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailPrecondition) || x.fake.Effects() != 0 {
			t.Fatalf("failure = %+v, effects %d", got, x.fake.Effects())
		}
		if es := x.effectStates(v.ID); len(es) != 1 || es[0] != db.EffectFailed {
			t.Fatalf("effects = %v, want one failed", es)
		}
	})
}

// insertProposal stores a pending proposal for a on the setup task as the
// database would hold one made by a buggy or hostile caller: it did not go
// through Create, so nothing checked it against the reservation.
func (x *nrW) insertProposal(id core.ProposalID, a coreprop.Action) {
	x.t.Helper()
	now := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
	if err := x.store.Commit(x.ctx, func(tx *db.Tx) error {
		cs, err := tx.EnsureChangeSet(x.ctx, nrSetup, "cs_nr", now)
		if err != nil {
			return err
		}
		return tx.CreateProposalRecord(x.ctx, coreprop.Proposal{ID: id, ChangeSet: cs, Task: nrSetup, Repo: nrRepo,
			Source: coreprop.UserSource{}, Action: a, ActionHash: coreprop.Hash(a), Created: now, State: coreprop.Pending{}})
	}); err != nil {
		x.t.Fatal(err)
	}
}

// v2 is the proposal's current view (its hash may have changed).
func v2(x *nrW, id core.ProposalID) View {
	x.t.Helper()
	v, err := x.svc.Get(x.ctx, id)
	if err != nil {
		x.t.Fatal(err)
	}
	return v
}

// relink points the operation at another proposal ID.
func (x *nrW) relink(rec db.ReservationRecord, id core.ProposalID) {
	x.t.Helper()
	if err := x.store.Commit(x.ctx, func(tx *db.Tx) error {
		st := newrepos.AwaitingConfirm{Spec: x.spec, Proposal: id, Hash: x.spec.Hash()}
		_, err := tx.SaveReservation(x.ctx, nrOp, st, id, rec.Operation.Version, rec.UpdatedAt.Add(1))
		return err
	}); err != nil {
		x.t.Fatal(err)
	}
}

func TestPropDuplicateConfirmNoSecondEffectNewRepo(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrAlreadyDecided) {
			t.Fatalf("a duplicate confirm = %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d confirms took effect, want 1", ok)
	}
	x.waitConfirmedAndRunning(v.ID)
	// The same attempt run a second time (a replay) calls nothing: the work is the worker's.
	p := proposalOf(t, x.world, v.ID)
	if out := x.svc.execNewRepo(x.ctx, p, 1); out.done || !out.pending {
		t.Fatalf("a second run of the same attempt = %+v", out)
	}
	if x.fake.Confirms() != 1 || x.fake.Effects() != 1 {
		t.Fatalf("confirms %d, effects %d, want 1 and 1", x.fake.Confirms(), x.fake.Effects())
	}
}

// An intent that is already recorded for the attempt (the process died after
// the record, and the call may have happened) is never followed by a second
// call of the same attempt.
func TestPropRecordedIntentBlocksASecondCallOfTheSameAttempt(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	x.confirmWithoutRunning(v)
	p := proposalOf(t, x.world, v.ID)
	tgt, _ := targetOf(p)
	if err := x.svc.beginHandoff(x.ctx, p, tgt, 1); err != nil {
		t.Fatal(err)
	}
	out := x.svc.execNewRepo(x.ctx, p, 1)
	if out.done || out.pending || out.kind != coreprop.FailInterrupted || !out.retryable {
		t.Fatalf("a repeated attempt = %+v", out)
	}
	if err := x.svc.beginHandoff(x.ctx, p, tgt, 1); !errors.Is(err, errEffectExists) {
		t.Fatalf("a second intent for the attempt = %v", err)
	}
	if len(x.fake.Calls()) != 0 {
		t.Fatalf("the lifecycle was called: %+v", x.fake.Calls())
	}
}

// ---- unknown outcomes are reconciled, never retried blindly ----

func TestPropUnknownNeverBlindRetryNewRepo(t *testing.T) {
	t.Run("a lost reply with an uncertain operation", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		once := sync.Once{}
		x.fake.OnChange(func(c proposalstest.Change) {
			x.mirror(c)
			if c.State.Phase == proposalstest.PhaseRunning {
				once.Do(func() { x.fake.GoUncertain(c.Op) })
			}
		})
		x.fake.Script(proposalstest.FailAfter, errors.New("the reply was lost"))
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable || got.Attempt != 1 {
			t.Fatalf("failure = %+v", got)
		}
		if x.fake.Confirms() != 1 || x.fake.Reconciles() == 0 {
			t.Fatalf("confirms %d, reconciles %d: a lost reply must only be reconciled", x.fake.Confirms(), x.fake.Reconciles())
		}
		if es := x.effectStates(v.ID); len(es) != 1 || es[0] != db.EffectUnknown {
			t.Fatalf("effects = %v, want one unknown", es)
		}
		// Retry while the outcome is still unknown reconciles again and confirms nothing.
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitForAttempt(v.ID, 2)
		if x.fake.Confirms() != 1 {
			t.Fatalf("a retry of an unknown outcome confirmed again (%d)", x.fake.Confirms())
		}
		// The read-only check finally proves it landed.
		x.fake.ScriptReconcile(proposalstest.PhaseReady, "")
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.fake.Confirms() != 1 || x.fake.Effects() != 1 {
			t.Fatalf("confirms %d, effects %d", x.fake.Confirms(), x.fake.Effects())
		}
	})
	t.Run("a lost reply that left the work running stays with the worker", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.fake.Script(proposalstest.FailAfter, errors.New("the reply was lost"))
		x.confirm(v)
		x.waitConfirmedAndRunning(v.ID)
		x.fake.Finish(nrOp)
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.fake.Confirms() != 1 {
			t.Fatalf("confirms = %d", x.fake.Confirms())
		}
	})
	t.Run("a failure before the call is a provable non-start and retry confirms once more", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.fake.Script(proposalstest.FailBefore, errors.New("connection refused"))
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
			t.Fatalf("failure = %+v", got)
		}
		if x.phase() != proposalstest.PhaseIdle || x.fake.Effects() != 0 {
			t.Fatal("work started after a failure before the call")
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitConfirmedAndRunning(v.ID)
		if x.fake.Confirms() != 2 || x.fake.Effects() != 1 {
			t.Fatalf("confirms %d, effects %d", x.fake.Confirms(), x.fake.Effects())
		}
	})
	t.Run("a refusal is definite", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.fake.Script(proposalstest.FailBefore, proposalstest.ErrFakeRefused{Msg: "The new repo service is busy."})
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailPrecondition) || !strings.Contains(got.Message, "busy") {
			t.Fatalf("failure = %+v", got)
		}
		if x.fake.Reconciles() != 0 {
			t.Fatal("a definite refusal was reconciled")
		}
	})
}

func (x *nrW) waitForAttempt(id core.ProposalID, n int) {
	x.t.Helper()
	(&p6{world: x.world}).waitForAttempt(id, n)
}

// ---- crash at every boundary ----

func TestNewRepoCrashAtEveryBoundary(t *testing.T) {
	t.Run("before the intent", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.confirmWithoutRunning(v)
		x.restart()
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
			t.Fatalf("failure = %+v", got)
		}
		if x.fake.Confirms() != 0 {
			t.Fatal("a restart confirmed the work on its own")
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitConfirmedAndRunning(v.ID)
		x.fake.Finish(nrOp)
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.fake.Confirms() != 1 {
			t.Fatalf("confirms = %d", x.fake.Confirms())
		}
	})
	t.Run("after the intent, before the call", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.confirmWithoutRunning(v)
		tgt, _ := targetOf(proposalOf(t, x.world, v.ID))
		if err := x.svc.beginHandoff(x.ctx, proposalOf(t, x.world, v.ID), tgt, 1); err != nil {
			t.Fatal(err)
		}
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if x.fake.Confirms() != 0 {
			t.Fatal("recovery confirmed the work")
		}
		if es := x.effectStates(v.ID); len(es) != 1 || es[0] != db.EffectFailed {
			t.Fatalf("effects = %v: the intent that provably never reached the lifecycle is settled failed", es)
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitConfirmedAndRunning(v.ID)
		if x.fake.Confirms() != 1 {
			t.Fatalf("confirms = %d", x.fake.Confirms())
		}
	})
	t.Run("after the call, work running", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.confirm(v)
		x.waitConfirmedAndRunning(v.ID)
		x.restart()
		x.settleQueue()
		if got := x.waitFor(v.ID, core.ProposalConfirmed); got.Status != core.ProposalConfirmed {
			t.Fatalf("view = %+v", got)
		}
		if x.fake.Confirms() != 1 {
			t.Fatalf("recovery confirmed again (%d)", x.fake.Confirms())
		}
		x.fake.Finish(nrOp)
		x.waitFor(v.ID, core.ProposalExecuted)
	})
	t.Run("after the call, the operation became uncertain", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.confirm(v)
		x.waitConfirmedAndRunning(v.ID)
		x.fake.GoUncertain(nrOp)
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if x.fake.Confirms() != 1 || x.fake.Reconciles() == 0 {
			t.Fatalf("confirms %d, reconciles %d", x.fake.Confirms(), x.fake.Reconciles())
		}
		x.fake.ScriptReconcile(proposalstest.PhaseReady, "")
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.fake.Confirms() != 1 {
			t.Fatalf("confirms = %d", x.fake.Confirms())
		}
	})
	t.Run("after the result, before the proposal was settled", func(t *testing.T) {
		x := newNR(t)
		v := x.mustCreate(x.initAction())
		x.confirm(v)
		x.waitConfirmedAndRunning(v.ID)
		// The operation finished while nobody was listening.
		x.fake.OnChange(func(c proposalstest.Change) { x.saveState(x.stateFor(c.State, v.ID)) })
		x.fake.Finish(nrOp)
		if got := v2(x, v.ID); got.Status != core.ProposalConfirmed {
			t.Fatalf("the proposal moved without being told: %+v", got)
		}
		x.restart()
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.fake.Confirms() != 1 {
			t.Fatalf("confirms = %d", x.fake.Confirms())
		}
	})
	t.Run("publish: the operation stopped while the process was down", func(t *testing.T) {
		x := newNR(t)
		x.toReady()
		v := x.mustCreate(x.publishAction())
		x.confirm(v)
		x.waitConfirmedAndRunning(v.ID)
		x.fake.OnChange(func(c proposalstest.Change) { x.saveState(x.stateFor(c.State, "")) })
		x.fake.Stop(nrOp, "name_collision")
		x.restart()
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable || x.fake.Confirms() != 1 {
			t.Fatalf("failure = %+v, confirms %d", got, x.fake.Confirms())
		}
	})
}

// ---- invariants ----

func TestInvariant_2_NewRepoNothingMutatesWithoutConfirmation(t *testing.T) {
	x := newNR(t)
	init := x.mustCreate(x.initAction())
	x.toReadyKeepingProposal()
	pub := x.mustCreate(x.publishAction())
	if _, err := x.svc.Reject(x.ctx, pub.ID, "user"); err != nil {
		t.Fatal(err)
	}
	// Created, rejected and refused proposals never reach the lifecycle.
	if _, err := x.svc.Confirm(x.ctx, init.ID, strings.Repeat("a", 64), "user"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Confirm with a wrong hash = %v", err)
	}
	if _, err := x.svc.Confirm(x.ctx, pub.ID, pub.ActionHash, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("Confirm of a rejected proposal = %v", err)
	}
	x.settleQueue()
	if n := len(x.fake.Calls()); n != 0 {
		t.Fatalf("the lifecycle was called %d times without a valid confirmation: %+v", n, x.fake.Calls())
	}
	if x.fake.Effects() != 0 {
		t.Fatal("work started without a confirmation")
	}
	for _, id := range []core.ProposalID{init.ID, pub.ID} {
		if es := x.effectStates(id); len(es) != 0 {
			t.Fatalf("an effect was recorded for %s without a confirmation: %v", id, es)
		}
	}
	// A restart never confirms either.
	x.restart()
	x.settleQueue()
	if len(x.fake.Calls()) != 0 {
		t.Fatal("a restart called the lifecycle")
	}
}

// toReadyKeepingProposal makes the repo ready for a publish while the pending
// init proposal stays pending (the test only checks that nothing runs).
func (x *nrW) toReadyKeepingProposal() {
	x.t.Helper()
	x.saveState(newrepos.Ready{Repo: nrRepo, InitialHead: nrHead})
	x.fake.AddReady(nrOp, x.pub.Hash())
}

func TestInvariant_4_NewRepoNoTokenInAuditLogOrRows(t *testing.T) {
	tokenErr := errors.New("request failed with token " + testToken)
	tokenRefusal := proposalstest.ErrFakeRefused{Msg: "refused with token " + testToken}
	for name, run := range map[string]func(x *nrW) core.ProposalID{
		"init lost reply": func(x *nrW) core.ProposalID {
			v := x.mustCreate(x.initAction())
			x.fake.Script(proposalstest.FailBefore, tokenErr)
			x.confirm(v)
			return v.ID
		},
		"init refusal": func(x *nrW) core.ProposalID {
			v := x.mustCreate(x.initAction())
			x.fake.Script(proposalstest.FailBefore, tokenRefusal)
			x.confirm(v)
			return v.ID
		},
		"publish lost reply": func(x *nrW) core.ProposalID {
			x.toReady()
			v := x.mustCreate(x.publishAction())
			x.fake.Script(proposalstest.FailBefore, tokenErr)
			x.confirm(v)
			return v.ID
		},
		"publish refusal": func(x *nrW) core.ProposalID {
			x.toReady()
			v := x.mustCreate(x.publishAction())
			x.fake.Script(proposalstest.FailBefore, tokenRefusal)
			x.confirm(v)
			return v.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newNR(t, testToken)
			id := run(x)
			x.waitFor(id, core.ProposalFailed)
			if text := x.visibleText(id); strings.Contains(text, testToken) {
				t.Fatalf("a token is visible:\n%s", text)
			}
			rows, err := x.store.ListEffectsByProposal(x.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range rows {
				if strings.Contains(string(e.Receipt)+e.Marker+e.RemoteID, testToken) {
					t.Fatalf("a token is in an effect row: %+v", e)
				}
			}
		})
	}
}

// A proposal for a reserved repo whose project was untracked never confirms.
func TestInitRepoNeedsATrackedProject(t *testing.T) {
	x := newNR(t)
	v := x.mustCreate(x.initAction())
	if err := x.store.Commit(x.ctx, func(tx *db.Tx) error {
		w := tx.Workspace()
		ws, err := w.Load(x.ctx)
		if err != nil {
			return err
		}
		pr := ws.Projects[x.spec.Project]
		return w.MarkProjectUntracked(x.ctx, corerepos.MarkProjectUntracked{ProjectID: x.spec.Project, At: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), ExpectVersion: pr.Version})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Confirm = %v, want a refusal (the project is not tracked)", err)
	}
	if x.fake.Confirms() != 0 {
		t.Fatal("the lifecycle was called for an untracked project")
	}
}
