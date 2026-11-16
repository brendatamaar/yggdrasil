package newrepos

import (
	"reflect"
	"strings"
	"testing"
)

func asInit(t *testing.T, o Operation) Initializing {
	t.Helper()
	st, ok := o.State.(Initializing)
	if !ok {
		t.Fatalf("state %T, want Initializing", o.State)
	}
	return st
}

func asPub(t *testing.T, o Operation) Publishing {
	t.Helper()
	st, ok := o.State.(Publishing)
	if !ok {
		t.Fatalf("state %T, want Publishing", o.State)
	}
	return st
}

// Row: none + Reserve.
func TestRowReserve(t *testing.T) {
	o, cmds := Step(Operation{Version: 7}, reserveIn())
	st, ok := o.State.(Reserved)
	if !ok || st.Hash != validInit().Hash() || o.ID != "op_1" || o.Project != "prj_1" || o.Repo != "rep_1" {
		t.Fatalf("got %+v", o)
	}
	if o.Version != 7 {
		t.Fatalf("Step must not bump the version, got %d", o.Version)
	}
	wantKinds(t, cmds, "PersistReservationAndSetupDraft", "Audit")
}

func TestRowReserveRefusals(t *testing.T) {
	cases := map[string]struct {
		mut    func(*Reserve)
		reason StopReason
	}{
		"project not tracked": {func(r *Reserve) { r.ProjectTracked = false }, StopUntracked},
		"leaf taken":          {func(r *Reserve) { r.LeafFree = false }, StopPathExists},
		"no author":           {func(r *Reserve) { r.HasAuthor = false }, StopMissingAuthor},
		"bad leaf":            {func(r *Reserve) { r.Spec.Leaf = "../x" }, StopPreconditions},
		"bad branch":          {func(r *Reserve) { r.Spec.Branch = "dev" }, StopPreconditions},
		"repo id mismatch":    {func(r *Reserve) { r.IDs.Repo = "rep_2" }, StopPreconditions},
		"empty proposal id":   {func(r *Reserve) { r.IDs.Proposal = "" }, StopPreconditions},
	}
	for name, c := range cases {
		in := reserveIn()
		c.mut(&in)
		o, cmds := Step(Operation{}, in)
		if o.State != nil || !reflect.DeepEqual(cmds, []Command{Explain{Reason: c.reason}}) {
			t.Errorf("%s: state %v commands %v", name, o.State, cmds)
		}
	}
	// Reserve when an operation already exists is ignored.
	r := reserved(t)
	o, cmds := Step(r, reserveIn())
	unchanged(t, r, o, cmds)
}

// Row: Reserved + ProposalCreated.
func TestRowProposalCreated(t *testing.T) {
	r := reserved(t)
	o, cmds := Step(r, ProposalCreated{ID: "pro_1", Hash: validInit().Hash()})
	st, ok := o.State.(AwaitingConfirm)
	if !ok || st.Proposal != "pro_1" || st.Hash != validInit().Hash() {
		t.Fatalf("got %+v", o.State)
	}
	wantKinds(t, cmds, "Audit", "PublishPreview")
	if cmds[1].(PublishPreview).Hash != st.Hash {
		t.Fatal("preview shows another hash")
	}
	for _, bad := range []ProposalCreated{{ID: "pro_1", Hash: "other"}, {ID: "", Hash: validInit().Hash()}} {
		o, cmds := Step(r, bad)
		unchanged(t, r, o, cmds)
	}
}

// Row: AwaitingConfirm + Confirmed.
func TestRowConfirmed(t *testing.T) {
	a := awaiting(t)
	o, cmds := Step(a, Confirmed{Hash: validInit().Hash(), Attempt: 3, Now: t0})
	st := asInit(t, o)
	if st.Attempt != 3 || st.Stage != StageCreateDirectory || len(st.Done) != 0 {
		t.Fatalf("got %+v", st)
	}
	wantKinds(t, cmds, "PersistIntent", "Audit", "RunInitStage")
	if run := cmds[2].(RunInitStage); run.Stage != StageCreateDirectory || run.Attempt != 3 || run.Spec != validInit() {
		t.Fatalf("run %+v", run)
	}
	for name, in := range map[string]Confirmed{
		"wrong hash": {Hash: "x", Attempt: 1}, "empty hash": {Attempt: 1}, "attempt zero": {Hash: validInit().Hash()},
		"negative attempt": {Hash: validInit().Hash(), Attempt: -1},
	} {
		o, cmds := Step(a, in)
		if !same(a, o) || len(cmds) != 0 {
			t.Errorf("%s: changed %+v %v", name, o.State, kinds(cmds))
		}
	}
	// Wrong state: Reserved is not yet confirmable.
	r := reserved(t)
	o, cmds = Step(r, Confirmed{Hash: validInit().Hash(), Attempt: 1})
	unchanged(t, r, o, cmds)
}

// Row: Initializing(a,s) + StageSucceeded(a,s), for every stage.
func TestRowInitSuccessEveryStage(t *testing.T) {
	stages := InitStages()
	for i, s := range stages {
		o := initializingAt(t, s)
		before := asInit(t, o)
		o2, cmds := Step(o, StageSucceeded{Attempt: 1, Stage: s, Receipt: initReceiptFor(before.Spec, s), Now: t0})
		if i == len(stages)-1 {
			rd, ok := o2.State.(Ready)
			if !ok || rd.Repo != "rep_1" || rd.InitialHead != strings.Repeat("a", 40) {
				t.Fatalf("final: %+v", o2.State)
			}
			wantKinds(t, cmds, "PersistReceipt", "Audit", "Audit")
			continue
		}
		next := asInit(t, o2)
		if next.Stage != stages[i+1] || next.Attempt != 1 || len(next.Done) != i+1 {
			t.Fatalf("stage %s -> %+v", s, next)
		}
		wantKinds(t, cmds, "PersistReceipt", "Audit", "RunInitStage")
		run := cmds[2].(RunInitStage)
		if run.Stage != stages[i+1] || len(run.Done) != i+1 || run.Attempt != 1 {
			t.Fatalf("run %+v", run)
		}
		if len(before.Done) != i {
			t.Fatalf("input operation mutated: %d receipts", len(before.Done))
		}
	}
}

func TestRowInitResultFenced(t *testing.T) {
	o := initializingAt(t, StageWriteREADME)
	st := asInit(t, o)
	rc := initReceiptFor(st.Spec, st.Stage)
	for name, in := range map[string]Input{
		"old attempt":   StageSucceeded{Attempt: 0, Stage: st.Stage, Receipt: rc},
		"new attempt":   StageSucceeded{Attempt: 2, Stage: st.Stage, Receipt: rc},
		"earlier stage": StageSucceeded{Attempt: 1, Stage: StageInitGit, Receipt: GitReceipt{GitDirIdentity: "x"}},
		"later stage":   StageSucceeded{Attempt: 1, Stage: StageWriteIgnore, Receipt: initReceiptFor(st.Spec, StageWriteIgnore)},
		"publish stage": StageSucceeded{Attempt: 1, Stage: StageCreateRemote, Receipt: rc},
		"nil stage":     StageSucceeded{Attempt: 1, Receipt: rc},
		"failed old":    StageFailed{Attempt: 9, Stage: st.Stage, Outcome: OutcomeDefiniteFailure},
		"failed stage":  StageFailed{Attempt: 1, Stage: StageInitGit, Outcome: OutcomeUnknown},
		"bad outcome":   StageFailed{Attempt: 1, Stage: st.Stage, Outcome: "weird"},
	} {
		o2, cmds := Step(o, in)
		if !same(o, o2) || len(cmds) != 0 {
			t.Errorf("%s: not ignored: %+v %v", name, o2.State, kinds(cmds))
		}
	}
}

// A wrong sealed variant, or one that does not match the confirmed spec, is a
// die-class input: Stopped, no stage command.
func TestRowWrongReceiptDies(t *testing.T) {
	o := initializingAt(t, StageWriteREADME)
	st := asInit(t, o)
	for name, rc := range map[string]Receipt{
		"dir for readme":     DirReceipt{Identity: "x"},
		"nil":                nil,
		"wrong file name":    FileReceipt{Name: ".gitignore", ContentHash: fileSum(st.Spec.README)},
		"wrong content hash": FileReceipt{Name: "README.md", ContentHash: fileSum("other")},
		"remote receipt":     RemoteReceipt{RemoteID: "1", URL: "https://github.com/a/b.git"},
	} {
		o2, cmds := Step(o, StageSucceeded{Attempt: 1, Stage: StageWriteREADME, Receipt: rc})
		sp, ok := o2.State.(Stopped)
		if !ok || sp.Reason != StopConflict {
			t.Errorf("%s: state %+v", name, o2.State)
			continue
		}
		if err := ValidateState(st.Spec, o2.State); err != nil {
			t.Errorf("%s: stopped state invalid: %v", name, err)
		}
		for _, c := range cmds {
			if _, run := c.(RunInitStage); run {
				t.Errorf("%s: ran a stage after an impossible input", name)
			}
		}
		wantKinds(t, cmds, "PersistFailure", "Audit", "Explain", "CancelWorker")
	}
}

// Row: Ready + PublishConfirmed.
func TestRowPublishConfirmed(t *testing.T) {
	r := readyOp(t)
	o, cmds := Step(r, publishConfirmIn(2))
	st := asPub(t, o)
	if st.Stage != StageCreateRemote || st.Attempt != 2 || st.Spec != validPublish() || st.Spec.Visibility != VisibilityPrivate {
		t.Fatalf("got %+v", st)
	}
	wantKinds(t, cmds, "PersistIntent", "Audit", "RunPublishStage")

	refusals := map[string]struct {
		mut    func(*PublishConfirmed)
		reason StopReason
		silent bool
	}{
		"origin exists": {func(p *PublishConfirmed) { p.OriginAbsent = false }, StopOriginExists, false},
		"head moved":    {func(p *PublishConfirmed) { p.HeadMatches = false }, StopPreconditions, false},
		"wrong hash":    {func(p *PublishConfirmed) { p.Hash = "x" }, "", true},
		"attempt zero":  {func(p *PublishConfirmed) { p.Attempt = 0 }, "", true},
		"visibility swapped after hash": {func(p *PublishConfirmed) {
			p.Spec.Visibility = VisibilityPublic
		}, "", true},
		"no visibility": {func(p *PublishConfirmed) {
			p.Spec.Visibility = ""
			p.Hash = p.Spec.Hash()
		}, "", true},
		"other repo": {func(p *PublishConfirmed) {
			p.Spec.Repo = "rep_9"
			p.Hash = p.Spec.Hash()
		}, "", true},
	}
	for name, c := range refusals {
		in := publishConfirmIn(1)
		c.mut(&in)
		o2, cmds := Step(r, in)
		if !same(r, o2) {
			t.Errorf("%s: changed state", name)
		}
		if c.silent && len(cmds) != 0 || !c.silent && !reflect.DeepEqual(cmds, []Command{Explain{Reason: c.reason}}) {
			t.Errorf("%s: commands %v", name, cmds)
		}
	}
	// Wrong state.
	a := awaiting(t)
	o3, cmds := Step(a, publishConfirmIn(1))
	unchanged(t, a, o3, cmds)
}

func TestPublishPublicOnlyWhenConfirmed(t *testing.T) {
	r := readyOp(t)
	in := publishConfirmIn(1)
	in.Spec.Visibility = VisibilityPublic
	in.Hash = in.Spec.Hash()
	o, cmds := Step(r, in)
	if st := asPub(t, o); st.Spec.Visibility != VisibilityPublic {
		t.Fatal("confirmed public spec was changed")
	}
	if run := cmds[2].(RunPublishStage); run.Spec.Visibility != VisibilityPublic {
		t.Fatal("command changed the confirmed visibility")
	}
}

// Row: Publishing(a,s) + StageSucceeded; the remote receipt travels on.
func TestRowPublishSuccessEveryStage(t *testing.T) {
	p := validPublish()
	o := publishingAt(t, StageCreateRemote)
	o, cmds := Step(o, StageSucceeded{Attempt: 1, Stage: StageCreateRemote, Receipt: remoteReceiptFor(p)})
	wantKinds(t, cmds, "PersistReceipt", "Audit", "RunPublishStage")
	run := cmds[2].(RunPublishStage)
	if run.Stage != StageAddOrigin || len(run.Done) != 1 || run.Done[0] != Receipt(remoteReceiptFor(p)) {
		t.Fatalf("add origin run %+v", run)
	}
	o, cmds = Step(o, StageSucceeded{Attempt: 1, Stage: StageAddOrigin, Receipt: OriginReceipt{URL: destURL(p)}})
	run = cmds[2].(RunPublishStage)
	if run.Stage != StagePushHead || len(run.Done) != 2 || run.Done[0] != Receipt(remoteReceiptFor(p)) {
		t.Fatalf("push run %+v", run)
	}
	o, cmds = Step(o, StageSucceeded{Attempt: 1, Stage: StagePushHead, Receipt: PushReceipt{Head: p.ExpectedHead}})
	pub, ok := o.State.(Published)
	if !ok || pub.RemoteID != "rid-"+p.Marker || pub.URL != destURL(p) || pub.Head != p.ExpectedHead {
		t.Fatalf("published %+v", o.State)
	}
	wantKinds(t, cmds, "PersistReceipt", "Audit", "Audit")
}

func TestRowPublishUnrelatedReceiptsDie(t *testing.T) {
	p := validPublish()
	cases := map[string]struct {
		at PublishStage
		rc Receipt
	}{
		"other owner remote":  {StageCreateRemote, RemoteReceipt{RemoteID: "1", URL: "https://github.com/other/thing.git"}},
		"credential url":      {StageCreateRemote, RemoteReceipt{RemoteID: "1", URL: "https://tok@github.com/acme/shop-new.git"}},
		"origin other remote": {StageAddOrigin, OriginReceipt{URL: "https://github.com/other/thing.git"}},
		"push other head":     {StagePushHead, PushReceipt{Head: strings.Repeat("d", 40)}},
		"push for origin":     {StageAddOrigin, PushReceipt{Head: p.ExpectedHead}},
	}
	for name, c := range cases {
		o := publishingAt(t, c.at)
		o2, cmds := Step(o, StageSucceeded{Attempt: 1, Stage: c.at, Receipt: c.rc})
		if sp, ok := o2.State.(Stopped); !ok || sp.Reason != StopConflict {
			t.Errorf("%s: %+v", name, o2.State)
		}
		for _, cmd := range cmds {
			if _, run := cmd.(RunPublishStage); run {
				t.Errorf("%s: ran a publish stage", name)
			}
		}
	}
}

// Rows: definite failure, unknown outcome, Recovered.
func TestRowFailureAndUncertain(t *testing.T) {
	for _, o := range []Operation{initializingAt(t, StageInitGit), publishingAt(t, StageAddOrigin)} {
		var attempt = 1
		var stage Stage
		switch st := o.State.(type) {
		case Initializing:
			stage = st.Stage
		case Publishing:
			stage = st.Stage
		}
		o2, cmds := Step(o, StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeDefiniteFailure})
		sp, ok := o2.State.(Stopped)
		if !ok || sp.Reason != StopStageFailed || sp.Stage != stage {
			t.Fatalf("stopped %+v", o2.State)
		}
		wantKinds(t, cmds, "PersistFailure", "Audit", "Explain")

		o3, _ := Step(o, StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeDefiniteFailure, Reason: StopHostRefused})
		if o3.State.(Stopped).Reason != StopHostRefused {
			t.Fatal("reason lost")
		}
		o3, _ = Step(o, StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeDefiniteFailure, Reason: "bogus"})
		if o3.State.(Stopped).Reason != StopStageFailed {
			t.Fatal("unknown reason accepted")
		}

		for name, in := range map[string]Input{
			"unknown outcome": StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeUnknown},
			"recovered":       Recovered{Now: t0},
		} {
			u, cmds := Step(o, in)
			un, ok := u.State.(Uncertain)
			if !ok || un.Attempt != attempt || un.Stage != stage {
				t.Fatalf("%s: %+v", name, u.State)
			}
			wantKinds(t, cmds, "PersistFailure", "Audit", "ReconcileStage")
			if rs := cmds[2].(ReconcileStage); rs.Attempt != attempt || rs.Stage != stage {
				t.Fatalf("%s: reconcile %+v", name, rs)
			}
		}
	}
}

// Rows: Uncertain + Reconciled.
func TestRowReconciled(t *testing.T) {
	// Init: exact success continues with the next stage.
	o := initializingAt(t, StageWriteREADME)
	st := asInit(t, o)
	u, _ := Step(o, Recovered{})
	o2, cmds := Step(u, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileExactSuccess, Receipt: initReceiptFor(st.Spec, StageWriteREADME)})
	next := asInit(t, o2)
	if next.Stage != StageWriteIgnore || len(next.Done) != 3 {
		t.Fatalf("after exact success: %+v", next)
	}
	wantKinds(t, cmds, "PersistReceipt", "Audit", "RunInitStage")

	// Exact success of the last init stage finishes.
	f := initializingAt(t, StageFinalizeTracking)
	fu, _ := Step(f, StageFailed{Attempt: 1, Stage: StageFinalizeTracking, Outcome: OutcomeUnknown})
	fo, _ := Step(fu, Reconciled{Attempt: 1, Stage: StageFinalizeTracking, Outcome: ReconcileExactSuccess, Receipt: TrackedReceipt{}})
	if _, ok := fo.State.(Ready); !ok {
		t.Fatalf("final reconcile: %+v", fo.State)
	}

	// Publish: exact success of the push publishes.
	p := validPublish()
	pu := publishingAt(t, StagePushHead)
	pu, _ = Step(pu, Recovered{})
	po, _ := Step(pu, Reconciled{Attempt: 1, Stage: StagePushHead, Outcome: ReconcileExactSuccess, Receipt: PushReceipt{Head: p.ExpectedHead}})
	if _, ok := po.State.(Published); !ok {
		t.Fatalf("publish reconcile: %+v", po.State)
	}

	// Absent, conflict: Stopped; unknown: unchanged, no commands.
	o4, cmds := Step(u, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileAbsent})
	if sp, ok := o4.State.(Stopped); !ok || sp.Reason != StopStageFailed {
		t.Fatalf("absent: %+v", o4.State)
	}
	wantKinds(t, cmds, "PersistFailure", "Audit", "Explain")
	o5, _ := Step(u, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileConflict})
	if sp, ok := o5.State.(Stopped); !ok || sp.Reason != StopConflict {
		t.Fatalf("conflict: %+v", o5.State)
	}
	o6, cmds := Step(u, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileUnknown})
	unchanged(t, u, o6, cmds)

	// Fencing and wrong variants.
	for name, in := range map[string]Reconciled{
		"old attempt": {Attempt: 0, Stage: StageWriteREADME, Outcome: ReconcileAbsent},
		"other stage": {Attempt: 1, Stage: StageInitGit, Outcome: ReconcileAbsent},
	} {
		o7, cmds := Step(u, in)
		if !same(u, o7) || len(cmds) != 0 {
			t.Errorf("%s: not ignored", name)
		}
	}
	o8, cmds := Step(u, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileExactSuccess, Receipt: DirReceipt{Identity: "x"}})
	if sp, ok := o8.State.(Stopped); !ok || sp.Reason != StopConflict {
		t.Fatalf("wrong variant: %+v %v", o8.State, kinds(cmds))
	}
	// A reconcile answer is ignored outside Uncertain.
	o9, cmds := Step(o, Reconciled{Attempt: 1, Stage: StageWriteREADME, Outcome: ReconcileAbsent})
	unchanged(t, o, o9, cmds)
}

// Row: Stopped/Uncertain + RetryConfirmed.
func TestRowRetry(t *testing.T) {
	o := initializingAt(t, StageWriteREADME)
	st := asInit(t, o)
	h := validInit().Hash()
	stopped, _ := Step(o, StageFailed{Attempt: 1, Stage: StageWriteREADME, Outcome: OutcomeDefiniteFailure})
	uncertain, _ := Step(o, Recovered{})

	for name, from := range map[string]Operation{"stopped": stopped, "uncertain": uncertain} {
		o2, cmds := Step(from, RetryConfirmed{Hash: h, Attempt: 2, PreconditionsHold: true})
		got := asInit(t, o2)
		if got.Attempt != 2 || got.Stage != StageWriteREADME || len(got.Done) != len(st.Done) {
			t.Fatalf("%s: %+v", name, got)
		}
		wantKinds(t, cmds, "PersistIntent", "Audit", "RunInitStage")
		run := cmds[2].(RunInitStage)
		if run.Attempt != 2 || run.Stage != StageWriteREADME || len(run.Done) != 2 {
			t.Fatalf("%s: run %+v", name, run)
		}
		for bn, in := range map[string]RetryConfirmed{
			"wrong hash":      {Hash: "x", Attempt: 2, PreconditionsHold: true},
			"attempt zero":    {Hash: h, Attempt: 0, PreconditionsHold: true},
			"same attempt":    {Hash: h, Attempt: 1, PreconditionsHold: true},
			"publish hash":    {Hash: validPublish().Hash(), Attempt: 2, PreconditionsHold: true},
			"empty hash":      {Attempt: 2, PreconditionsHold: true},
			"old attempt (0)": {Hash: h, Attempt: -3, PreconditionsHold: true},
		} {
			o3, cmds := Step(from, in)
			if !same(from, o3) || len(cmds) != 0 {
				t.Errorf("%s/%s: changed", name, bn)
			}
		}
		o4, cmds := Step(from, RetryConfirmed{Hash: h, Attempt: 2, PreconditionsHold: false})
		if !same(from, o4) || !reflect.DeepEqual(cmds, []Command{Explain{Reason: StopPreconditions}}) {
			t.Errorf("%s: preconditions not enforced: %v", name, cmds)
		}
	}

	// Publish retry.
	po := publishingAt(t, StageAddOrigin)
	ps, _ := Step(po, StageFailed{Attempt: 1, Stage: StageAddOrigin, Outcome: OutcomeDefiniteFailure})
	pr, cmds := Step(ps, RetryConfirmed{Hash: validPublish().Hash(), Attempt: 4, PreconditionsHold: true})
	pst := asPub(t, pr)
	if pst.Stage != StageAddOrigin || pst.Attempt != 4 || len(pst.Done) != 1 {
		t.Fatalf("publish retry %+v", pst)
	}
	wantKinds(t, cmds, "PersistIntent", "Audit", "RunPublishStage")

	// Rejected and untracked operations never resume.
	rej, _ := Step(awaiting(t), Reject{})
	unt, _ := Step(o, Untracked{})
	for name, from := range map[string]Operation{"rejected": rej, "untracked": unt} {
		o5, cmds := Step(from, RetryConfirmed{Hash: h, Attempt: 9, PreconditionsHold: true})
		unchanged(t, from, o5, cmds)
		_ = name
	}
	// Retry in a state that is neither Stopped nor Uncertain.
	o6, cmds := Step(o, RetryConfirmed{Hash: h, Attempt: 9, PreconditionsHold: true})
	unchanged(t, o, o6, cmds)
}

// Rows: Reject and Untracked.
func TestRowRejectAndUntracked(t *testing.T) {
	for name, o := range map[string]Operation{"reserved": reserved(t), "awaiting": awaiting(t)} {
		o2, cmds := Step(o, Reject{Now: t0})
		if sp, ok := o2.State.(Stopped); !ok || sp.Reason != StopRejected || sp.Stage != nil {
			t.Fatalf("%s: %+v", name, o2.State)
		}
		wantKinds(t, cmds, "PersistFailure", "Audit")
	}
	for name, o := range map[string]Operation{"init": initializingAt(t, StageInitGit), "ready": readyOp(t), "published": func() Operation {
		p, _ := Step(publishingAt(t, StagePushHead), StageSucceeded{Attempt: 1, Stage: StagePushHead, Receipt: PushReceipt{Head: validPublish().ExpectedHead}})
		return p
	}()} {
		o2, cmds := Step(o, Reject{})
		unchanged(t, o, o2, cmds)
		_ = name
	}

	nonTerminal := map[string]Operation{
		"reserved": reserved(t), "awaiting": awaiting(t), "init": initializingAt(t, StageWriteIgnore), "ready": readyOp(t),
		"publishing": publishingAt(t, StagePushHead),
	}
	nonTerminal["uncertain"], _ = Step(initializingAt(t, StageInitGit), Recovered{})
	for name, o := range nonTerminal {
		o2, cmds := Step(o, Untracked{Now: t0})
		sp, ok := o2.State.(Stopped)
		if !ok || sp.Reason != StopUntracked {
			t.Fatalf("%s: %+v", name, o2.State)
		}
		wantKinds(t, cmds, "PersistFailure", "Audit", "CancelWorker")
		if err := ValidateStateShape(o2.State); err != nil {
			t.Errorf("%s: stopped state shape: %v", name, err)
		}
		// Deleting anything is unrepresentable: no command type deletes; check the list is exactly these.
		o3, cmds := Step(o2, Untracked{})
		unchanged(t, o2, o3, cmds)
	}
	for name, o := range map[string]Operation{
		"none": {}, "published": func() Operation {
			p, _ := Step(publishingAt(t, StagePushHead), StageSucceeded{Attempt: 1, Stage: StagePushHead, Receipt: PushReceipt{Head: validPublish().ExpectedHead}})
			return p
		}(),
	} {
		o2, cmds := Step(o, Untracked{})
		if !same(o, o2) || len(cmds) != 0 {
			t.Errorf("%s: changed", name)
		}
	}
}

// Every pair that the table does not list is unchanged with no commands.
func TestUnlistedPairsAreInert(t *testing.T) {
	published, _ := Step(publishingAt(t, StagePushHead), StageSucceeded{Attempt: 1, Stage: StagePushHead, Receipt: PushReceipt{Head: validPublish().ExpectedHead}})
	rejected, _ := Step(reserved(t), Reject{})
	h := validInit().Hash()
	inputs := []Input{
		Reserve{Spec: validInit(), IDs: testIDs(), ProjectTracked: true, LeafFree: true, HasAuthor: true},
		ProposalCreated{ID: "pro_1", Hash: h}, Confirmed{Hash: h, Attempt: 1},
		StageSucceeded{Attempt: 1, Stage: StageCreateDirectory, Receipt: DirReceipt{Identity: "x"}},
		StageFailed{Attempt: 1, Stage: StageCreateDirectory, Outcome: OutcomeUnknown},
		publishConfirmIn(1), Recovered{}, Reconciled{Attempt: 1, Stage: StageCreateDirectory, Outcome: ReconcileAbsent},
		RetryConfirmed{Hash: h, Attempt: 5, PreconditionsHold: true}, Reject{},
	}
	for name, o := range map[string]Operation{"published": published, "rejected": rejected} {
		for _, in := range inputs {
			o2, cmds := Step(o, in)
			if !same(o, o2) || len(cmds) != 0 {
				t.Errorf("%s + %T: changed", name, in)
			}
		}
	}
	// An Uncertain operation does not take a late success of the same attempt.
	u, _ := Step(initializingAt(t, StageInitGit), Recovered{})
	o2, cmds := Step(u, StageSucceeded{Attempt: 1, Stage: StageInitGit, Receipt: GitReceipt{GitDirIdentity: "x"}})
	unchanged(t, u, o2, cmds)
	o3, cmds := Step(u, StageFailed{Attempt: 1, Stage: StageInitGit, Outcome: OutcomeDefiniteFailure})
	unchanged(t, u, o3, cmds)
	o4, cmds := Step(u, Recovered{})
	unchanged(t, u, o4, cmds)
	// Unknown input type.
	o5, cmds := Step(u, nil)
	unchanged(t, u, o5, cmds)
}

// Step never aliases or mutates the receipts of its input.
func TestStepDoesNotMutateInput(t *testing.T) {
	o := initializingAt(t, StageWriteIgnore)
	snap := asInit(t, o)
	before := append([]Receipt{}, snap.Done...)
	o2, cmds := Step(o, StageSucceeded{Attempt: 1, Stage: StageWriteIgnore, Receipt: initReceiptFor(snap.Spec, StageWriteIgnore)})
	next := asInit(t, o2)
	next.Done[0] = TrackedReceipt{}
	cmds[2].(RunInitStage).Done[1] = TrackedReceipt{}
	if !reflect.DeepEqual(asInit(t, o).Done, before) {
		t.Fatal("input receipts changed")
	}
}
