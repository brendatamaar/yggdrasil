package newrepos

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

const (
	propRuns  = 300
	propSteps = 90
)

// Property names (one per design invariant).
const (
	propInitOnlyAfterConfirm  = "InitOnlyAfterConfirm"
	propStageAttemptFenced    = "StageAttemptFenced"
	propPublishPrivateDefault = "PublishPrivateDefault"
	propNoUnrelatedRemote     = "NoUnrelatedRemoteAdoption"
	propUnknownNeverBlind     = "UnknownNeverBlindRetry"
	propInitNeverOverwrites   = "InitNeverOverwrites"
	propStructure             = "Structure"
)

type mode struct {
	noConfirm  bool // every initialization confirmation carries a wrong hash
	badPublish bool // every publish confirmation is wrong, upgraded or has no visibility
}

type pending struct {
	attempt int
	stage   Stage
}

type world struct {
	r    *rand.Rand
	m    mode
	init InitSpec
	pub  PublishSpec
	op   Operation

	attempt int
	hist    []Input
	viol    map[string][]string

	confirmedInit bool         // oracle: a matching Confirmed was delivered in AwaitingConfirm
	confirmedPub  *PublishSpec // oracle: a valid PublishConfirmed was delivered in Ready
	pend          *pending
	ran           map[string]bool
}

func newWorld(seed int64, m mode) *world {
	r := rand.New(rand.NewSource(seed))
	pub := validPublish()
	if r.Intn(2) == 0 {
		pub.Visibility = VisibilityPublic
	}
	return &world{r: r, m: m, init: validInit(), pub: pub, viol: map[string][]string{}, ran: map[string]bool{}}
}

func (w *world) fail(prop, format string, a ...any) {
	if len(w.viol[prop]) < 3 {
		w.viol[prop] = append(w.viol[prop], fmt.Sprintf(format, a...))
	}
}

func (w *world) fresh() int { w.attempt++; return w.attempt }

func (w *world) allStages() []Stage {
	var out []Stage
	out = append(out, stagesOf(InitStages())...)
	return append(out, stagesOf(PublishStages())...)
}

func (w *world) validReceipt(s Stage) Receipt {
	if v, ok := s.(InitStage); ok {
		return initReceiptFor(w.init, v)
	}
	if v, ok := s.(PublishStage); ok {
		return publishReceiptFor(w.pub, v)
	}
	return nil
}

func (w *world) badReceipt(s Stage) Receipt {
	other := w.allStages()[w.r.Intn(9)]
	pool := []Receipt{
		nil, w.validReceipt(other), DirReceipt{}, TrackedReceipt{},
		RemoteReceipt{RemoteID: "9", URL: "https://github.com/other/thing.git"},
		RemoteReceipt{RemoteID: "9", URL: "https://tok@github.com/acme/shop-new.git"},
		OriginReceipt{URL: "https://github.com/other/thing.git"},
		PushReceipt{Head: strings.Repeat("d", 40)},
		FileReceipt{Name: "README.md", ContentHash: fileSum("other")},
	}
	return pool[w.r.Intn(len(pool))]
}

func (w *world) receipt(s Stage) Receipt {
	if w.r.Intn(8) == 0 {
		return w.badReceipt(s)
	}
	return w.validReceipt(s)
}

func resumeHash(r Resumable) string {
	if v, ok := r.(ResumeInit); ok {
		return v.Spec.Hash()
	}
	if v, ok := r.(ResumePublish); ok {
		return v.Spec.Hash()
	}
	return "none"
}

func (w *world) initHash(h string) string {
	if w.m.noConfirm {
		return "wrong-" + h
	}
	return h
}

func (w *world) publishConfirm() PublishConfirmed {
	r := w.r
	in := PublishConfirmed{Spec: w.pub, Hash: w.pub.Hash(), Attempt: w.fresh(), OriginAbsent: r.Intn(6) != 0, HeadMatches: r.Intn(6) != 0}
	if w.m.badPublish {
		switch r.Intn(4) {
		case 0:
			in.Hash = "wrong"
		case 1: // visibility changed after the hash was computed
			if in.Spec.Visibility == VisibilityPrivate {
				in.Spec.Visibility = VisibilityPublic
			} else {
				in.Spec.Visibility = VisibilityPrivate
			}
		case 2: // no visibility chosen, even with a matching hash
			in.Spec.Visibility = ""
			in.Hash = in.Spec.Hash()
		default: // another repo's spec with its own hash
			in.Spec.Repo = "rep_other"
			in.Hash = in.Spec.Hash()
		}
	}
	return in
}

func (w *world) stageInput(attempt int, stage Stage) Input {
	r := w.r
	switch n := r.Intn(22); {
	case n < 12:
		return StageSucceeded{Attempt: attempt, Stage: stage, Receipt: w.receipt(stage)}
	case n < 14:
		reasons := []StopReason{"", StopHostRefused, StopNameCollision, StopPathExists, "bogus"}
		return StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeDefiniteFailure, Reason: reasons[r.Intn(len(reasons))]}
	case n < 17:
		return StageFailed{Attempt: attempt, Stage: stage, Outcome: OutcomeUnknown}
	case n < 20:
		return Recovered{}
	case n < 21:
		return Untracked{}
	default:
		return Reject{}
	}
}

func (w *world) targeted() Input {
	r := w.r
	switch st := w.op.State.(type) {
	case nil:
		in := reserveIn()
		in.Spec = w.init
		switch r.Intn(10) {
		case 0:
			in.ProjectTracked = false
		case 1:
			in.LeafFree = false
		case 2:
			in.HasAuthor = false
		case 3:
			in.Spec.Leaf = "con"
		}
		return in
	case Reserved:
		return ProposalCreated{ID: "pro_1", Hash: st.Hash}
	case AwaitingConfirm:
		switch r.Intn(12) {
		case 0:
			return Reject{}
		case 1:
			return Untracked{}
		}
		return Confirmed{Hash: w.initHash(st.Hash), Attempt: w.fresh()}
	case Initializing:
		return w.stageInput(st.Attempt, st.Stage)
	case Publishing:
		return w.stageInput(st.Attempt, st.Stage)
	case Ready:
		switch r.Intn(14) {
		case 0:
			return Untracked{}
		case 1:
			return Reject{}
		}
		return w.publishConfirm()
	case Uncertain:
		switch n := r.Intn(12); {
		case n < 6:
			outs := []ReconcileOutcome{ReconcileExactSuccess, ReconcileExactSuccess, ReconcileAbsent, ReconcileConflict, ReconcileUnknown, "weird"}
			return Reconciled{Attempt: st.Attempt, Stage: st.Stage, Outcome: outs[r.Intn(len(outs))], Receipt: w.receipt(st.Stage)}
		case n < 9:
			return RetryConfirmed{Hash: w.initHash(resumeHash(st.Resume)), Attempt: w.fresh(), PreconditionsHold: r.Intn(4) != 0}
		case n < 10:
			return Untracked{}
		default:
			return Recovered{}
		}
	case Stopped:
		switch r.Intn(10) {
		case 0:
			return Untracked{}
		case 1:
			return Recovered{}
		}
		return RetryConfirmed{Hash: w.initHash(resumeHash(st.Resume)), Attempt: w.fresh(), PreconditionsHold: r.Intn(4) != 0}
	case Published:
	}
	return []Input{Recovered{}, Untracked{}, Reject{}, Confirmed{Hash: "x", Attempt: 1}}[r.Intn(4)]
}

func (w *world) perturb(in Input) Input {
	r := w.r
	if v, ok := in.(StageSucceeded); ok {
		switch r.Intn(3) {
		case 0:
			v.Attempt += r.Intn(5) - 2
		case 1:
			v.Stage = w.allStages()[r.Intn(9)]
		default:
			v.Receipt = w.badReceipt(v.Stage)
		}
		return v
	}
	if v, ok := in.(StageFailed); ok {
		if r.Intn(2) == 0 {
			v.Attempt += r.Intn(5) - 2
		} else {
			v.Stage = w.allStages()[r.Intn(9)]
		}
		return v
	}
	if v, ok := in.(Reconciled); ok {
		if r.Intn(2) == 0 {
			v.Attempt += r.Intn(5) - 2
		} else {
			v.Stage = w.allStages()[r.Intn(9)]
		}
		return v
	}
	if v, ok := in.(Confirmed); ok {
		v.Hash, v.Attempt = "wrong", r.Intn(3)-1
		return v
	}
	if v, ok := in.(RetryConfirmed); ok {
		v.Attempt = r.Intn(3) - 1
		return v
	}
	if v, ok := in.(PublishConfirmed); ok {
		v.Attempt = r.Intn(3) - 1
		return v
	}
	return in
}

func (w *world) gen() Input {
	if len(w.hist) > 0 && w.r.Intn(12) == 0 {
		return w.hist[w.r.Intn(len(w.hist))] // duplicate or stale delivery
	}
	in := w.targeted()
	if w.r.Intn(6) == 0 {
		in = w.perturb(in)
	}
	return in
}

func curStage(st State) (attempt int, stage Stage, ok bool) {
	v := describe(st)
	if v.kind == kindRunning || v.kind == kindUncertain {
		return v.attempt, v.stage, true
	}
	return 0, nil, false
}

func rank(c Command) int {
	if _, ok := c.(RunInitStage); ok {
		return 2
	}
	if _, ok := c.(RunPublishStage); ok {
		return 2
	}
	if _, ok := c.(ReconcileStage); ok {
		return 2
	}
	if _, ok := c.(CancelWorker); ok {
		return 2
	}
	if _, ok := c.(PublishPreview); ok {
		return 1
	}
	if _, ok := c.(Explain); ok {
		return 1
	}
	return 0 // the records
}

func isRun(c Command) (attempt int, stage Stage, ok bool) {
	if v, ok := c.(RunInitStage); ok {
		return v.Attempt, v.Stage, true
	}
	if v, ok := c.(RunPublishStage); ok {
		return v.Attempt, v.Stage, true
	}
	return 0, nil, false
}

func (w *world) step() {
	in := w.gen()
	prev := w.op
	next, cmds := Step(prev, in)
	w.check(prev, in, next, cmds)
	w.op = next
	w.hist = append(w.hist, in)
}

func (w *world) check(prev Operation, in Input, next Operation, cmds []Command) {
	inert := same(prev, next) && len(cmds) == 0

	// Structure: valid states, header and version, command order.
	if next.State != nil {
		if err := ValidateState(w.init, next.State); err != nil {
			w.fail(propStructure, "invalid state after %T: %v (%+v)", in, err, next.State)
		}
	}
	if next.Version != prev.Version {
		w.fail(propStructure, "version changed")
	}
	if prev.State != nil && (next.ID != prev.ID || next.Project != prev.Project || next.Repo != prev.Repo) {
		w.fail(propStructure, "header changed")
	}
	for i := 1; i < len(cmds); i++ {
		if rank(cmds[i]) < rank(cmds[i-1]) {
			w.fail(propStructure, "an effect precedes a record after %T: %v", in, kinds(cmds))
		}
	}
	for _, c := range cmds {
		if pi, ok := c.(PersistIntent); ok {
			hasRun := false
			for _, d := range cmds {
				if a, s, ok := isRun(d); ok && a == pi.Attempt && s == pi.Stage {
					hasRun = true
				}
			}
			if !hasRun {
				w.fail(propStructure, "intent without its stage")
			}
		}
		if _, _, ok := isRun(c); ok {
			// the intent must come before the effect in the same list (or the
			// state change itself carries it, for a continuation)
			a, s, _ := isRun(c)
			na, ns, ok := curStage(next.State)
			if !ok || na != a || ns != s {
				w.fail(propStageAttemptFenced, "run %d/%v does not match new state %+v", a, s, next.State)
			}
		}
	}

	// Oracles from the delivered input (independent of Step's output).
	_, awaiting := prev.State.(AwaitingConfirm)
	if c, ok := in.(Confirmed); ok && awaiting && c.Hash == prev.State.(AwaitingConfirm).Hash && c.Attempt >= 1 {
		w.confirmedInit = true
	}
	if p, ok := in.(PublishConfirmed); ok {
		if _, ready := prev.State.(Ready); ready && p.Hash == p.Spec.Hash() && p.Spec.Validate() == nil &&
			p.Spec.Repo == w.pub.Repo && p.OriginAbsent && p.HeadMatches && p.Attempt >= 1 && p.Spec == w.pub {
			spec := p.Spec
			w.confirmedPub = &spec
		}
	}

	// Fencing.
	a, st, running := curStage(prev.State)
	_, uncertain := prev.State.(Uncertain)
	if v, ok := in.(StageSucceeded); ok {
		if (!running || uncertain || v.Attempt != a || v.Stage != st) && !inert {
			w.fail(propStageAttemptFenced, "stale StageSucceeded(%d,%v) accepted in %T", v.Attempt, v.Stage, prev.State)
		}
	}
	if v, ok := in.(StageFailed); ok {
		if (!running || uncertain || v.Attempt != a || v.Stage != st) && !inert {
			w.fail(propStageAttemptFenced, "stale StageFailed(%d,%v) accepted in %T", v.Attempt, v.Stage, prev.State)
		}
	}
	if v, ok := in.(Reconciled); ok {
		if (!running || !uncertain || v.Attempt != a || v.Stage != st) && !inert {
			w.fail(propStageAttemptFenced, "stale Reconciled(%d,%v) accepted in %T", v.Attempt, v.Stage, prev.State)
		}
	}
	if pi, ok := prev.State.(Initializing); ok {
		if ni, ok := next.State.(Initializing); ok && ni.Attempt == pi.Attempt && !inert {
			if len(ni.Done) != len(pi.Done)+1 || !reflect.DeepEqual(ni.Done[:len(pi.Done)], pi.Done) {
				w.fail(propInitNeverOverwrites, "receipts were rewritten: %d -> %d", len(pi.Done), len(ni.Done))
			}
		}
	}

	// Commands that start effects.
	_, prevUncertain := prev.State.(Uncertain)
	_, prevStopped := prev.State.(Stopped)
	for _, c := range cmds {
		a, s, isRunCmd := isRun(c)
		if !isRunCmd {
			continue
		}
		if v, ok := c.(RunInitStage); ok {
			if !w.confirmedInit {
				w.fail(propInitOnlyAfterConfirm, "RunInitStage(%v) before a matching confirmation (%T)", v.Stage, in)
			}
			if v.Spec != w.init {
				w.fail(propInitOnlyAfterConfirm, "RunInitStage for a spec the user did not confirm")
			}
			if err := validatePrefix(v.Stage, v.Done); err != nil {
				w.fail(propInitNeverOverwrites, "stage %v has receipts %d: %v", v.Stage, len(v.Done), err)
			}
		}
		if v, ok := c.(RunPublishStage); ok {
			if w.confirmedPub == nil {
				w.fail(propPublishPrivateDefault, "RunPublishStage(%v) without a valid confirmation (%T)", v.Stage, in)
			} else if v.Spec != *w.confirmedPub {
				w.fail(propPublishPrivateDefault, "publish spec differs from the confirmed one: %+v vs %+v", v.Spec, *w.confirmedPub)
			}
			if v.Spec.Visibility != w.pub.Visibility {
				w.fail(propPublishPrivateDefault, "visibility changed from %s to %s", w.pub.Visibility, v.Spec.Visibility)
			}
			if err := validatePrefix(v.Stage, v.Done); err != nil {
				w.fail(propInitNeverOverwrites, "publish stage %v has receipts %d: %v", v.Stage, len(v.Done), err)
			}
			if v.Stage == StageAddOrigin || v.Stage == StagePushHead {
				mine := Receipt(remoteReceiptFor(w.pub))
				if len(v.Done) == 0 || v.Done[0] != mine {
					w.fail(propNoUnrelatedRemote, "%v runs without the remote this operation created: %+v", v.Stage, v.Done)
				}
				if v.Stage == StagePushHead && (len(v.Done) < 2 || v.Done[1] != Receipt(OriginReceipt{URL: destURL(w.pub)})) {
					w.fail(propNoUnrelatedRemote, "push runs without this operation's origin: %+v", v.Done)
				}
			}
		}
		key := fmt.Sprintf("%d/%s", a, s.Name())
		if w.ran[key] {
			w.fail(propInitNeverOverwrites, "stage %s ran twice in attempt %d", s.Name(), a)
		}
		w.ran[key] = true

		// A stage that may have partly run is never restarted without a fresh confirmation.
		retry, isRetry := in.(RetryConfirmed)
		rc, isRec := in.(Reconciled)
		_, isConfirm := in.(Confirmed)
		_, isSucc := in.(StageSucceeded)
		_, isPub := in.(PublishConfirmed)
		if !isRetry && !isRec && !isConfirm && !isSucc && !isPub {
			w.fail(propUnknownNeverBlind, "%T started a stage", in)
		}
		if isRec && rc.Outcome != ReconcileExactSuccess {
			w.fail(propUnknownNeverBlind, "a reconcile answer %s started a stage", rc.Outcome)
		}
		if prevUncertain || prevStopped {
			switch {
			case isRetry:
				if pa, ok := w.pendingAttempt(prev); ok && retry.Attempt <= pa {
					w.fail(propUnknownNeverBlind, "retry with a non-fresh attempt %d <= %d", retry.Attempt, pa)
				}
				if !retry.PreconditionsHold || retry.Hash != resumeHashOf(prev.State) {
					w.fail(propUnknownNeverBlind, "retry without a fresh matching confirmation")
				}
			case isRec:
				if w.pend != nil && s == w.pend.stage {
					w.fail(propUnknownNeverBlind, "the uncertain stage %s was repeated after a reconcile", s.Name())
				}
			default:
				w.fail(propUnknownNeverBlind, "%T started a stage from %T", in, prev.State)
			}
		}
	}

	// An unknown outcome only asks to reconcile.
	if v, ok := in.(StageFailed); ok && v.Outcome == OutcomeUnknown {
		w.checkUnknown(prev, next, cmds, in)
	}
	if _, ok := in.(Recovered); ok {
		w.checkUnknown(prev, next, cmds, in)
	}
	if _, ok := next.State.(Uncertain); !ok {
		w.pend = nil
	}
}

func resumeHashOf(st State) string {
	if v := describe(st); v.kind == kindUncertain || v.kind == kindStopped {
		return resumeHash(v.resume)
	}
	return ""
}

func (w *world) pendingAttempt(prev Operation) (int, bool) {
	if v := describe(prev.State); v.kind == kindUncertain || v.kind == kindStopped {
		return v.attempt, true
	}
	return 0, false
}

func (w *world) checkUnknown(prev, next Operation, cmds []Command, in Input) {
	a, s, running := curStage(prev.State)
	_, uncertain := prev.State.(Uncertain)
	if !running || uncertain {
		return
	}
	if sf, ok := in.(StageFailed); ok && (sf.Attempt != a || sf.Stage != s) {
		return // fenced; covered elsewhere
	}
	u, ok := next.State.(Uncertain)
	if !ok || u.Attempt != a || u.Stage != s {
		w.fail(propUnknownNeverBlind, "%T in %T did not become Uncertain: %+v", in, prev.State, next.State)
		return
	}
	if !reflect.DeepEqual(kinds(cmds), []string{"PersistFailure", "Audit", "ReconcileStage"}) {
		w.fail(propUnknownNeverBlind, "unknown outcome commands: %v", kinds(cmds))
	}
	w.pend = &pending{attempt: a, stage: s}
}

func runProp(t *testing.T, prop string, m mode) (reached map[string]int) {
	t.Helper()
	reached = map[string]int{}
	for seed := int64(1); seed <= propRuns; seed++ {
		w := newWorld(seed, m)
		for i := 0; i < propSteps; i++ {
			w.step()
			reached[fmt.Sprintf("%T", w.op.State)]++
		}
		if v := w.viol[prop]; len(v) > 0 {
			t.Fatalf("seed %d violates %s: %s", seed, prop, strings.Join(v, "; "))
		}
		if v := w.viol[propStructure]; len(v) > 0 && prop != propStructure {
			t.Fatalf("seed %d violates structure: %s", seed, strings.Join(v, "; "))
		}
	}
	return reached
}

func TestPropStructure(t *testing.T) {
	reached := runProp(t, propStructure, mode{})
	for _, name := range []string{"newrepos.Reserved", "newrepos.AwaitingConfirm", "newrepos.Initializing", "newrepos.Ready",
		"newrepos.Publishing", "newrepos.Published", "newrepos.Uncertain", "newrepos.Stopped", "<nil>"} {
		if reached[name] == 0 {
			t.Errorf("the generator never reached %s", name)
		}
	}
}

func TestPropInitOnlyAfterConfirm(t *testing.T) {
	runProp(t, propInitOnlyAfterConfirm, mode{})
	// Without a matching confirmation nothing ever starts.
	reached := runProp(t, propInitOnlyAfterConfirm, mode{noConfirm: true})
	for _, name := range []string{"newrepos.Initializing", "newrepos.Ready", "newrepos.Uncertain", "newrepos.Publishing"} {
		if reached[name] != 0 {
			t.Fatalf("reached %s without a confirmation", name)
		}
	}
}

func TestPropStageAttemptFenced(t *testing.T) { runProp(t, propStageAttemptFenced, mode{}) }

func TestPropPublishPrivateDefault(t *testing.T) {
	runProp(t, propPublishPrivateDefault, mode{})
	reached := runProp(t, propPublishPrivateDefault, mode{badPublish: true})
	for _, name := range []string{"newrepos.Publishing", "newrepos.Published"} {
		if reached[name] != 0 {
			t.Fatalf("reached %s with only invalid publish confirmations", name)
		}
	}
	if reached["newrepos.Ready"] == 0 {
		t.Fatal("the bad-confirmation runs never reached Ready")
	}
}

func TestPropNoUnrelatedRemoteAdoption(t *testing.T) { runProp(t, propNoUnrelatedRemote, mode{}) }

func TestPropUnknownNeverBlindRetry(t *testing.T) { runProp(t, propUnknownNeverBlind, mode{}) }

func TestPropInitNeverOverwrites(t *testing.T) { runProp(t, propInitNeverOverwrites, mode{}) }

// Crash and restart: Recovered at every stage boundary of both flows, then each
// reconciliation outcome.
func TestPropCrashAtEveryStageBoundary(t *testing.T) {
	type flow struct {
		name   string
		run    string
		stages []Stage
		start  func(*testing.T, Stage) Operation
		hash   string
		done   func(Operation) bool
	}
	flows := []flow{
		{"init", "RunInitStage", stagesOf(InitStages()), func(t *testing.T, s Stage) Operation { return initializingAt(t, s.(InitStage)) }, validInit().Hash(),
			func(o Operation) bool { _, ok := o.State.(Ready); return ok }},
		{"publish", "RunPublishStage", stagesOf(PublishStages()), func(t *testing.T, s Stage) Operation { return publishingAt(t, s.(PublishStage)) }, validPublish().Hash(),
			func(o Operation) bool { _, ok := o.State.(Published); return ok }},
	}
	for _, f := range flows {
		for k, stage := range f.stages {
			t.Run(fmt.Sprintf("%s/%s", f.name, stage.Name()), func(t *testing.T) {
				o := f.start(t, stage)
				u, cmds := Step(o, Recovered{Now: t0})
				if _, ok := u.State.(Uncertain); !ok {
					t.Fatalf("restart: %+v", u.State)
				}
				wantKinds(t, cmds, "PersistFailure", "Audit", "ReconcileStage")
				// Restart again while uncertain: nothing moves, and nothing runs.
				u2, cmds := Step(u, Recovered{})
				unchanged(t, u, u2, cmds)
				// Late result of the old attempt does not move it.
				u3, cmds := Step(u, StageSucceeded{Attempt: 1, Stage: stage, Receipt: nil})
				unchanged(t, u, u3, cmds)

				// Unknown: still Uncertain, no write.
				uu, cmds := Step(u, Reconciled{Attempt: 1, Stage: stage, Outcome: ReconcileUnknown})
				unchanged(t, u, uu, cmds)

				// Exact success: the rest runs once, each stage exactly once, to the end.
				cur, cmds := Step(u, Reconciled{Attempt: 1, Stage: stage, Outcome: ReconcileExactSuccess, Receipt: receiptOf(t, stage)})
				seen := map[string]int{}
				for i := k + 1; ; i++ {
					for _, c := range cmds {
						if a, s, ok := isRun(c); ok {
							seen[s.Name()]++
							if a != 1 || s != f.stages[i] {
								t.Fatalf("ran %v attempt %d, want %v", s, a, f.stages[i])
							}
						}
					}
					if f.done(cur) {
						break
					}
					a, s, _ := curStage(cur.State)
					cur, cmds = Step(cur, StageSucceeded{Attempt: a, Stage: s, Receipt: receiptOf(t, s)})
				}
				for n, c := range seen {
					if c != 1 {
						t.Fatalf("stage %s ran %d times", n, c)
					}
				}
				if len(seen) != len(f.stages)-k-1 {
					t.Fatalf("ran %d stages after the exact success, want %d", len(seen), len(f.stages)-k-1)
				}

				// Absent and conflict: Stopped; a fresh confirmation retries the same stage with a new attempt.
				for _, out := range []ReconcileOutcome{ReconcileAbsent, ReconcileConflict} {
					sp, _ := Step(u, Reconciled{Attempt: 1, Stage: stage, Outcome: out})
					if _, ok := sp.State.(Stopped); !ok {
						t.Fatalf("%s: %+v", out, sp.State)
					}
					bad, cmds := Step(sp, RetryConfirmed{Hash: "x", Attempt: 2, PreconditionsHold: true})
					unchanged(t, sp, bad, cmds)
					r, cmds := Step(sp, RetryConfirmed{Hash: f.hash, Attempt: 2, PreconditionsHold: true})
					if out == ReconcileConflict {
						// A conflicting finding is the die class: no new effect, ever.
						unchanged(t, sp, r, cmds)
						continue
					}
					a, s, _ := curStage(r.State)
					if a != 2 || s != stage {
						t.Fatalf("%s retry: %+v", out, r.State)
					}
					wantKinds(t, cmds, "PersistIntent", "Audit", f.run)
				}
			})
		}
	}
}

// receiptOf is the valid receipt for stage in the operation's specs.
func receiptOf(t *testing.T, s Stage) Receipt {
	t.Helper()
	switch v := s.(type) {
	case InitStage:
		return initReceiptFor(validInit(), v)
	case PublishStage:
		return publishReceiptFor(validPublish(), v)
	default:
		t.Fatalf("no receipt for %v", s)
		return nil
	}
}
