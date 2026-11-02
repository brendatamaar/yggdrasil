package changesets_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
)

// three proposals: commit in rep_1, commit and push in rep_2.
func threeProposals(r *rig) (ids [3]core.ProposalID, seen map[core.ProposalID]string) {
	seen = map[core.ProposalID]string{}
	for i, p := range []struct {
		repo core.RepoID
		kind core.ProposalKind
	}{{rep1, core.ProposalCommit}, {rep2, core.ProposalCommit}, {rep2, core.ProposalPush}} {
		id, h := r.add(p.repo, p.kind)
		ids[i], seen[id] = id, h
	}
	return ids, seen
}

func TestRunConfirmsOneProposalAtATimeInMergeOrder(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	v, err := r.confirmAll(seen)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.State.(cs.Confirming); !ok {
		t.Fatalf("state %T", v.State)
	}
	if got := r.confirmedIDs(); !slices.Equal(got, ids[:1]) {
		t.Fatalf("after the start: %v", got)
	}
	r.reconcile() // the first is only confirmed, not executed: nothing more
	if got := r.confirmedIDs(); len(got) != 1 {
		t.Fatalf("a second proposal was confirmed before the first finished: %v", got)
	}
	for i := 0; i < 2; i++ {
		r.w.SetStatus(ids[i], core.ProposalExecuted, "")
		r.reconcile()
		if got := r.confirmedIDs(); !slices.Equal(got, ids[:i+2]) {
			t.Fatalf("step %d: confirmed %v", i, got)
		}
	}
	r.w.SetStatus(ids[2], core.ProposalExecuted, "")
	r.reconcile()
	v = r.view()
	sh, ok := v.State.(cs.Shipped)
	if !ok || sh.LastRun != (cs.RunCompleted{}) {
		t.Fatalf("final state %#v", v.State)
	}
	for _, c := range r.api.Calls() {
		if c.By != "user:confirm-all" {
			t.Fatalf("confirmation actor %q", c.By)
		}
	}
	if n := len(r.w.Audit("changeset:" + string(set))); n < 4 {
		t.Fatalf("%d audit entries for a whole run", n)
	}
	if r.hub.SetEvents() == 0 {
		t.Fatal("the UI was not told")
	}
}

func TestRunNeedsTheHashesTheUserSaw(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	seen[ids[1]] = changesetstestHash(42)
	_, err := r.confirmAll(seen)
	if err == nil || userMessage(err) == "" {
		t.Fatalf("a wrong hash was accepted: %v", err)
	}
	delete(seen, ids[2])
	if _, err := r.confirmAll(seen); err == nil {
		t.Fatal("a partial list was accepted")
	}
	if len(r.api.Calls()) != 0 {
		t.Fatal("something was confirmed")
	}
	if _, ok := r.view().State.(cs.Open); !ok {
		t.Fatalf("state %T", r.view().State)
	}
}

func changesetstestHash(n int) string {
	return strings.Repeat("0", 62) + string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestRunHaltsAtTheFirstFailure(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	r.w.SetStatus(ids[0], core.ProposalFailed, "")
	r.reconcile()
	r.reconcile()
	st, ok := r.view().State.(cs.Open)
	if !ok {
		t.Fatalf("state %T", r.view().State)
	}
	if h, ok := st.LastRun.(cs.RunHalted); !ok || h.At != ids[0] {
		t.Fatalf("last run %#v", st.LastRun)
	}
	if len(r.confirmedIDs()) != 1 {
		t.Fatalf("confirmed after a failure: %v", r.confirmedIDs())
	}
}

func TestRefusedConfirmationHaltsWithTheReason(t *testing.T) {
	r := newRig(t)
	_, seen := threeProposals(r)
	r.api.Fail, r.api.FailTimes = &changesets.Refused{Message: "That repo is no longer tracked."}, 1
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	h, ok := openOf(r.view().State).LastRun.(cs.RunHalted)
	if !ok || h.Reason != "That repo is no longer tracked." {
		t.Fatalf("last run %#v", openOf(r.view().State).LastRun)
	}
}

func TestTransientFailureIsRetriedOnceThenHalts(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	r.api.Fail, r.api.FailTimes = errors.New("database is locked"), 1
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.view().State.(cs.Confirming); !ok {
		t.Fatalf("a transient error ended the run: %T", r.view().State)
	}
	r.reconcile() // retry succeeds
	if got := r.confirmedIDs(); !slices.Equal(got, ids[:1]) {
		t.Fatalf("after the retry: %v", got)
	}

	r2 := newRig(t)
	_, seen2 := threeProposals(r2)
	r2.api.Fail, r2.api.FailTimes = errors.New("database is locked"), 5
	if _, err := r2.confirmAll(seen2); err != nil {
		t.Fatal(err)
	}
	r2.reconcile()
	if _, ok := openOf(r2.view().State).LastRun.(cs.RunHalted); !ok {
		t.Fatalf("two transient failures did not halt: %#v", r2.view().State)
	}
}

func TestStateIsCommittedBeforeTheConfirmationIsSent(t *testing.T) {
	r := newRig(t)
	_, seen := threeProposals(r)
	r.w.FailCommits = 1
	if _, err := r.confirmAll(seen); err == nil {
		t.Fatal("a failed commit was reported as success")
	}
	if len(r.api.Calls()) != 0 {
		t.Fatal("a proposal was confirmed although the run was not stored")
	}
}

func TestCrashBeforeTheConfirmationIsSentResumesAfterRestart(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	r.api.Fail, r.api.FailTimes = errors.New("killed"), 1
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	// Restart: a new service on the same stored rows; the failure counter is gone.
	r.svc = r.newService()
	r.reconcile()
	if got := r.confirmedIDs(); !slices.Equal(got, ids[:1]) {
		t.Fatalf("after the restart: %v", got)
	}
}

func TestCrashAfterTheConfirmationLandedDoesNotConfirmTwice(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	r.svc = r.newService() // restart: confirmed, running
	r.reconcile()
	r.reconcile()
	if got := r.confirmedIDs(); len(got) != 1 {
		t.Fatalf("restart re-confirmed: %v", got)
	}
	// The proposal finished while the process was down.
	r.w.SetStatus(ids[0], core.ProposalExecuted, "")
	r.svc = r.newService()
	r.reconcile()
	if got := r.confirmedIDs(); !slices.Equal(got, ids[:2]) {
		t.Fatalf("after the restart: %v", got)
	}
}

func TestResendOfAConfirmationThatHadLandedIsNotARefusal(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	// The confirmation landed, but the store of the set still shows pending
	// (stale read): the resend is "already decided", which must not halt.
	r.w.SetStatus(ids[0], core.ProposalConfirmed, "")
	r.reconcile()
	if _, ok := r.view().State.(cs.Confirming); !ok {
		t.Fatalf("state %T", r.view().State)
	}
}

func TestDuplicateDeliveryChangesNothing(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	r.w.SetStatus(ids[0], core.ProposalExecuted, "")
	for i := 0; i < 4; i++ {
		if err := r.svc.OnProposalChanged(context.Background(), ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.confirmedIDs(); !slices.Equal(got, ids[:2]) {
		t.Fatalf("duplicates confirmed %v", got)
	}
	n := len(r.w.Audit("changeset:" + string(set)))
	r.reconcile()
	if len(r.w.Audit("changeset:"+string(set))) != n {
		t.Fatal("a no-change pass wrote audit entries")
	}
}

func TestConcurrentConfirmStartsOneRun(t *testing.T) {
	r := newRig(t)
	_, seen := threeProposals(r)
	var wg sync.WaitGroup
	var ok, refused int
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.confirmAll(seen)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if userMessage(err) != "" {
				refused++
			}
		}()
	}
	wg.Wait()
	if ok != 1 || refused != 7 || len(r.api.Calls()) != 1 {
		t.Fatalf("ok=%d refused=%d calls=%d", ok, refused, len(r.api.Calls()))
	}
}

func TestStopRunLeavesTheRestPending(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.StopRun(context.Background(), set, "user"); err != nil {
		t.Fatal(err)
	}
	r.w.SetStatus(ids[0], core.ProposalExecuted, "")
	r.reconcile()
	if len(r.confirmedIDs()) != 1 {
		t.Fatalf("a stopped run went on: %v", r.confirmedIDs())
	}
	if _, err := r.svc.StopRun(context.Background(), set, "user"); err == nil {
		t.Fatal("stopping with no run is not a mistake the user is told about")
	}
}

func TestOrderIsStoredAndFrozenDuringARun(t *testing.T) {
	r := newRig(t)
	_, seen := threeProposals(r)
	ctx := context.Background()
	for _, bad := range [][]string{nil, {"rep_1"}, {"rep_1", "rep_1"}, {"rep_1", "rep_9"}} {
		if _, err := r.svc.SetOrder(ctx, set, bad, "user"); err == nil || userMessage(err) == "" {
			t.Fatalf("order %v accepted: %v", bad, err)
		}
	}
	if _, err := r.svc.SetOrder(ctx, set, []string{"rep_2", "rep_1"}, "user"); err != nil {
		t.Fatal(err)
	}
	if got := r.view().State.(cs.Open).Set.Order; !slices.Equal(got, []core.RepoID{rep2, rep1}) {
		t.Fatalf("order %v", got)
	}
	if got := r.w.SetRow(set).MergeOrder; !slices.Equal(got, []core.RepoID{rep2, rep1}) {
		t.Fatalf("stored order %v", got)
	}
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SetOrder(ctx, set, []string{"rep_1", "rep_2"}, "user"); err == nil {
		t.Fatal("the order changed during a run")
	}
	// The run follows the stored order: rep_2's proposals first.
	if r.api.Calls()[0].ID != "prp_002" {
		t.Fatalf("first confirmed %s", r.api.Calls()[0].ID)
	}
}

func TestDependencyWarningNeedsAcknowledgement(t *testing.T) {
	r := newRig(t)
	// rep_1 depends on rep_2, but rep_1 is ordered first.
	r.deps.Edge = []cs.Edge{{Dependent: rep1, Dependency: rep2}}
	_, seen := threeProposals(r)
	v := r.view()
	if len(v.Warnings) == 0 || !slices.Equal(v.Suggested, []core.RepoID{rep2, rep1}) {
		t.Fatalf("warnings %v suggested %v", v.Warnings, v.Suggested)
	}
	if _, err := r.confirmAll(seen); err == nil || !strings.Contains(userMessage(err), "depends") {
		t.Fatalf("unacknowledged run: %v", err)
	}
	if len(r.api.Calls()) != 0 {
		t.Fatal("confirmed without acknowledgement")
	}
	if _, err := r.confirmAll(seen, string(cs.WarningDependentBeforeDependency)); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range r.w.Audit("changeset:" + string(set)) {
		found = found || strings.Contains(string(e.Detail), "acknowledged")
	}
	if !found {
		t.Fatal("the acknowledgement is not in the audit log")
	}
}

func TestDependencyCycleFallsBackAndIsReported(t *testing.T) {
	r := newRig(t)
	r.deps.Edge = []cs.Edge{{Dependent: rep1, Dependency: rep2}, {Dependent: rep2, Dependency: rep1}}
	threeProposals(r)
	v := r.view()
	if len(v.Cycle) != 2 || !slices.Equal(v.Suggested, []core.RepoID{rep1, rep2}) {
		t.Fatalf("cycle %v suggested %v", v.Cycle, v.Suggested)
	}
}

func TestUnreadableDependenciesMeanNoWarnings(t *testing.T) {
	r := newRig(t)
	r.deps.Err = errors.New("pom.xml is broken")
	r.deps.Edge = []cs.Edge{{Dependent: rep1, Dependency: rep2}}
	_, seen := threeProposals(r)
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatalf("an unreadable manifest blocked the run: %v", err)
	}
}

func TestLinksComeOnlyFromExecutedMergeRequests(t *testing.T) {
	r := newRig(t)
	a, _ := r.add(rep1, core.ProposalMergeRequest)
	b, _ := r.add(rep2, core.ProposalMergeRequest)
	c, _ := r.add(rep2, core.ProposalPush)
	r.w.SetStatus(a, core.ProposalExecuted, "https://git.example.com/acme/shop-api/-/merge_requests/3")
	r.w.SetStatus(b, core.ProposalFailed, "https://git.example.com/acme/shop-web/-/merge_requests/9")
	r.w.SetStatus(c, core.ProposalExecuted, "https://git.example.com/other")
	links := r.view().State.(cs.Open).Links
	if len(links) != 1 || links[rep1] == "" {
		t.Fatalf("links %v", links)
	}
}

func TestSetShipsWhenEveryMemberIsDoneAndReopensOnANewProposal(t *testing.T) {
	r := newRig(t)
	a, _ := r.add(rep1, core.ProposalCommit)
	r.w.SetStatus(a, core.ProposalExecuted, "")
	r.reconcile()
	if _, ok := r.view().State.(cs.Shipped); !ok {
		t.Fatalf("state %T", r.view().State)
	}
	if r.w.SetRow(set).Status != core.ChangeSetShipped {
		t.Fatal("the status column was not updated")
	}
	b, _ := r.add(rep1, core.ProposalPush)
	if err := r.svc.OnProposalChanged(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.view().State.(cs.Open); !ok || r.w.SetRow(set).Status != core.ChangeSetOpen {
		t.Fatalf("state %T", r.view().State)
	}
}

func TestCloseNeedsNothingPending(t *testing.T) {
	r := newRig(t)
	a, _ := r.add(rep1, core.ProposalCommit)
	if _, err := r.svc.Close(context.Background(), set, "user"); err == nil || userMessage(err) == "" {
		t.Fatalf("closed with a pending proposal: %v", err)
	}
	r.w.SetStatus(a, core.ProposalRejected, "")
	if _, err := r.svc.Close(context.Background(), set, "user"); err != nil {
		t.Fatal(err)
	}
}

func TestInconsistentRowsAreRefusedNotGuessed(t *testing.T) {
	r := newRig(t)
	r.add("rep_zzz", core.ProposalCommit) // fine: the repo joins the set
	if _, err := r.svc.Get(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Get(context.Background(), "cs_none"); err == nil {
		t.Fatal("an unknown set was found")
	}
}
