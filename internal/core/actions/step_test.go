package actions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func cmdTypes(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.TrimPrefix(reflect.TypeOf(c).String(), "actions."))
	}
	return out
}

func wantCmds(t *testing.T, name string, cmds []Command, want ...string) {
	t.Helper()
	got := cmdTypes(cmds)
	if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("%s: commands %v, want %v", name, got, want)
	}
}

func wantState[S State](t *testing.T, name string, d Submission) S {
	t.Helper()
	s, ok := d.State.(S)
	if !ok {
		var zero S
		t.Fatalf("%s: state %T, want %T", name, d.State, zero)
	}
	return s
}

func wantRefused(t *testing.T, name string, d Submission, why RefuseReason) {
	t.Helper()
	if r := wantState[Refused](t, name, d); r.Reason != why {
		t.Errorf("%s: refused for %s, want %s", name, r.Reason, why)
	}
}

func wantUnchanged(t *testing.T, name string, before, after Submission, cmds []Command) {
	t.Helper()
	if !reflect.DeepEqual(before, after) || len(cmds) != 0 {
		t.Errorf("%s: changed to %+v with %v", name, after, cmdTypes(cmds))
	}
}

// One case per row of the transition table of docs/design/phase-6-actions.md.
func TestTransitionTable(t *testing.T) {
	const k = DiagnosePipeline

	t.Run("Idle Requested to Preparing", func(t *testing.T) {
		for _, d0 := range []Submission{{}, {State: Idle{}}} { // a nil state is Idle
			d, cmds := Step(d0, Requested{Request: reqFor(k), IDs: idsFor(k), Now: epoch})
			if p := wantState[Preparing](t, "requested", d); p.Attempt != 1 {
				t.Errorf("attempt %d", p.Attempt)
			}
			wantCmds(t, "requested", cmds, "Audit", "LoadFactsAndContext")
			if l := cmds[1].(LoadFactsAndContext); l.Attempt != 1 || l.Request != reqFor(k) {
				t.Errorf("load = %+v", l)
			}
			if d.Digest != reqFor(k).Digest() || d.Request != reqFor(k) || d.IDs.Task != "tsk_1" || d.Version != 1 || d.Seq != 1 {
				t.Errorf("stored %+v", d)
			}
		}
	})
	t.Run("Idle Requested invalid refuses", func(t *testing.T) {
		bad := reqFor(k)
		bad.Kind = "force_push"
		d, cmds := Step(Submission{}, Requested{Request: bad, IDs: idsFor(k)})
		wantRefused(t, "bad request", d, RefuseInvalid)
		wantCmds(t, "bad request", cmds, "Explain", "Audit", "Publish")

		d, cmds = Step(Submission{}, Requested{Request: reqFor(k), IDs: IDs{}})
		wantRefused(t, "no ids", d, RefuseInvalid)
		wantCmds(t, "no ids", cmds, "Explain", "Audit", "Publish")

		d, _ = Step(Submission{}, Requested{Request: reqFor(PullAndCheck), IDs: IDs{Task: "tsk_1"}})
		wantRefused(t, "pull without proposal id", d, RefuseInvalid)
		d, _ = Step(Submission{}, Requested{Request: reqFor(PullAndCheck), IDs: IDs{Proposals: []core.ProposalID{"p_1", "p_1"}}})
		wantRefused(t, "duplicate proposal ids", d, RefuseInvalid)
	})

	t.Run("Preparing Prepared to Ready", func(t *testing.T) {
		d, cmds := Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: factsFor(k), Context: readyCtx()})
		r := wantState[Ready](t, "prepared", d)
		if r.ExpectedHead != "c1" || r.Context.Availability != ContextAvailable {
			t.Errorf("ready = %+v", r)
		}
		wantCmds(t, "prepared", cmds, "PresentPreview")
	})
	t.Run("Preparing Prepared for a late attempt is ignored", func(t *testing.T) {
		d := to(k, "preparing")
		d2, cmds := Step(d, Prepared{Attempt: 2, Facts: factsFor(k), Context: readyCtx()})
		wantUnchanged(t, "late prepared", d, d2, cmds)
	})
	t.Run("Preparing Prepared refuses a changed scope", func(t *testing.T) {
		f := factsFor(k)
		f.Repo = "rep_2"
		d, cmds := Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: f, Context: readyCtx()})
		wantRefused(t, "other repo", d, RefuseScopeChanged)
		wantCmds(t, "other repo", cmds, "Explain", "Audit", "Publish")

		f = factsFor(k)
		f.CIRunID = "78" // a newer pipeline replaced the one the user chose
		d, _ = Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: f, Context: readyCtx()})
		wantRefused(t, "new pipeline", d, RefuseScopeChanged)

		f = factsFor(k)
		f.Reasons = nil
		f.CIRunID = ""
		d, _ = Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: f, Context: readyCtx()})
		wantRefused(t, "no longer failing", d, RefuseNoOffering)
	})
	t.Run("Preparing Prepared import needs the issue capability", func(t *testing.T) {
		f := factsFor(ImportIssue)
		f.Caps.HostIssues = false
		d, _ := Step(to(ImportIssue, "preparing"), Prepared{Attempt: 1, Facts: f, Context: PreparedContext{Availability: ContextNone}})
		wantRefused(t, "no issues", d, RefuseCapability)
		d, _ = Step(to(ImportIssue, "preparing"), Prepared{Attempt: 1, Facts: factsFor(ImportIssue), Context: PreparedContext{Availability: ContextNone}})
		r := wantState[Ready](t, "import", d)
		if r.Context.WaitingForFolder {
			t.Error("an import waits for a folder")
		}
	})
	t.Run("Preparing Prepared normalises the context", func(t *testing.T) {
		f := factsFor(k)
		f.Caps.HostLogs = false // the shell said available, the facts say no log
		big := PreparedContext{Text: strings.Repeat("é", MaxContextBytes), Availability: ContextAvailable}
		d, _ := Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: f, Context: big})
		c := wantState[Ready](t, "context", d).Context
		if len(c.Text) > MaxContextBytes || !c.Truncated || c.Text == "" {
			t.Errorf("text %d bytes, truncated=%v", len(c.Text), c.Truncated)
		}
		if c.Availability != ContextMetadataOnly || len(c.Unavailable) == 0 {
			t.Errorf("a missing log is claimed available: %+v", c)
		}
		f = factsFor(k)
		f.Readable = false
		d, _ = Step(to(k, "preparing"), Prepared{Attempt: 1, Facts: f, Context: readyCtx()})
		if !wantState[Ready](t, "unreadable", d).Context.WaitingForFolder {
			t.Error("an unreadable repo is not marked as waiting for its folder")
		}
	})

	t.Run("Preparing PrepareFailed to Refused", func(t *testing.T) {
		d, cmds := Step(to(k, "preparing"), PrepareFailed{Attempt: 1, Reason: RefuseUntracked})
		wantRefused(t, "failed", d, RefuseUntracked)
		wantCmds(t, "failed", cmds, "Explain", "Audit", "Publish")
		d, _ = Step(to(k, "preparing"), PrepareFailed{Attempt: 1, Reason: "garbage"})
		wantRefused(t, "garbage", d, RefusePrepareFailed)
		d0 := to(k, "preparing")
		d2, cmds := Step(d0, PrepareFailed{Attempt: 9, Reason: RefuseUntracked})
		wantUnchanged(t, "late failure", d0, d2, cmds)
	})

	t.Run("Ready SubmitRequested to Submitting", func(t *testing.T) {
		d, cmds := Step(to(k, "ready"), SubmitRequested{ScopeHead: "c1", Now: epoch})
		s := wantState[Submitting](t, "submit", d)
		if s.Attempt != 2 || s.Key != "action:req_1:2" {
			t.Errorf("submitting = %+v", s)
		}
		wantCmds(t, "submit", cmds, "PersistIntent", "Audit", "Dispatch")
		disp := cmds[2].(Dispatch)
		if disp.Key != s.Key || disp.Attempt != 2 || disp.Request != reqFor(k) || disp.IDs.Task != "tsk_1" || disp.Context.Text != "log" {
			t.Errorf("dispatch = %+v", disp)
		}
		if in := cmds[0].(PersistIntent); in.Digest != reqFor(k).Digest() || in.Key != s.Key {
			t.Errorf("intent = %+v", in)
		}
	})
	t.Run("Ready SubmitRequested with a moved head refuses", func(t *testing.T) {
		d, cmds := Step(to(k, "ready"), SubmitRequested{ScopeHead: "c2"})
		wantRefused(t, "stale", d, RefuseStaleHead)
		wantCmds(t, "stale", cmds, "Explain", "Audit", "Publish")
	})
	t.Run("SubmitRequested elsewhere", func(t *testing.T) {
		for _, name := range []string{"idle", "preparing", "completed", "refused", "cancelled"} {
			d := to(k, name)
			d2, cmds := Step(d, SubmitRequested{ScopeHead: "c1"})
			wantUnchanged(t, name, d, d2, cmds)
		}
	})
	t.Run("Submitting SubmitRequested looks up, never redispatches", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, SubmitRequested{ScopeHead: "c1"})
		if !reflect.DeepEqual(d, d2) {
			t.Error("a double click changed the state")
		}
		wantCmds(t, "double click", cmds, "ReconcileDispatch")
	})

	t.Run("Submitting Submitted to Completed", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Submitted{Attempt: d.Seq, Result: resultFor(k)})
		if c := wantState[Completed](t, "done", d2); c.Result.TaskID != "tsk_1" {
			t.Errorf("result %+v", c.Result)
		}
		wantCmds(t, "done", cmds, "PersistResult", "Audit", "Publish")
	})
	t.Run("Submitted for a pull completes with its proposal", func(t *testing.T) {
		d := to(PullAndCheck, "submitting")
		d2, _ := Step(d, Submitted{Attempt: d.Seq, Result: resultFor(PullAndCheck)})
		if c := wantState[Completed](t, "pull", d2); len(c.Result.ProposalIDs) != 1 {
			t.Errorf("result %+v", c.Result)
		}
	})
	t.Run("Submitted for an old attempt is ignored", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Submitted{Attempt: d.Seq - 1, Result: resultFor(k)})
		wantUnchanged(t, "late result", d, d2, cmds)
	})
	t.Run("Submitted with a result of another shape reconciles", func(t *testing.T) {
		d := to(k, "submitting")
		for _, bad := range []Result{{}, {ProposalIDs: []core.ProposalID{"prp_1"}}, {TaskID: "tsk_1", ProposalIDs: []core.ProposalID{"prp_1"}}, {TaskID: "bad id"}} {
			d2, cmds := Step(d, Submitted{Attempt: d.Seq, Result: bad})
			if !reflect.DeepEqual(d, d2) {
				t.Errorf("%+v completed the request", bad)
			}
			wantCmds(t, "bad result", cmds, "ReconcileDispatch")
		}
		p := to(PullAndCheck, "submitting")
		p2, _ := Step(p, Submitted{Attempt: p.Seq, Result: Result{TaskID: "tsk_1"}})
		if !reflect.DeepEqual(p, p2) {
			t.Error("a pull completed with a task")
		}
	})

	t.Run("Submitting SubmitFailed definite to Refused", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, SubmitFailed{Attempt: d.Seq, Outcome: SubmitDefiniteRefusal, Reason: RefuseCapability})
		wantRefused(t, "refusal", d2, RefuseCapability)
		wantCmds(t, "refusal", cmds, "PersistResult", "Explain", "Audit", "Publish")
		d2, _ = Step(d, SubmitFailed{Attempt: d.Seq, Outcome: SubmitDefiniteRefusal, Reason: "x"})
		wantRefused(t, "unknown reason", d2, RefuseAdmission)
	})
	t.Run("Submitting SubmitFailed unknown reconciles", func(t *testing.T) {
		d := to(k, "submitting")
		for _, o := range []SubmitOutcome{SubmitUnknown, "weird"} {
			d2, cmds := Step(d, SubmitFailed{Attempt: d.Seq, Outcome: o, Reason: RefuseAdmission})
			if !reflect.DeepEqual(d, d2) {
				t.Errorf("outcome %q changed the state", o)
			}
			wantCmds(t, "unknown", cmds, "ReconcileDispatch")
		}
		d2, cmds := Step(d, SubmitFailed{Attempt: 1, Outcome: SubmitDefiniteRefusal})
		wantUnchanged(t, "late failure", d, d2, cmds)
	})

	t.Run("Recovered", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Recovered{Now: epoch})
		if !reflect.DeepEqual(d, d2) {
			t.Error("recovery changed the state")
		}
		wantCmds(t, "recovered", cmds, "ReconcileDispatch")
		if r := cmds[0].(ReconcileDispatch); r.Attempt != d.Seq || r.Key != "action:req_1:2" {
			t.Errorf("reconcile = %+v", r)
		}
		for _, name := range []string{"idle", "preparing", "ready", "completed", "refused", "cancelled"} {
			d := to(k, name)
			d2, cmds := Step(d, Recovered{})
			wantUnchanged(t, "recovered "+name, d, d2, cmds)
		}
	})

	t.Run("Reconciled found completes", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Reconciled{Attempt: d.Seq, Outcome: ReconcileFound, Result: resultFor(k)})
		wantState[Completed](t, "found", d2)
		wantCmds(t, "found", cmds, "PersistResult", "Audit", "Publish")
	})
	t.Run("Reconciled found with an impossible record stops", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Reconciled{Attempt: d.Seq, Outcome: ReconcileFound, Result: Result{}})
		wantUnchanged(t, "found nothing", d, d2, cmds)
	})
	t.Run("Reconciled absent refuses", func(t *testing.T) {
		d := to(k, "submitting")
		d2, cmds := Step(d, Reconciled{Attempt: d.Seq, Outcome: ReconcileAbsent})
		wantRefused(t, "absent", d2, RefuseAbsentAfterLoss)
		wantCmds(t, "absent", cmds, "Explain", "Audit", "Publish")
	})
	t.Run("Reconciled no answer waits", func(t *testing.T) {
		d := to(k, "submitting")
		for _, o := range []ReconcileOutcome{ReconcileNoAnswer, "weird"} {
			d2, cmds := Step(d, Reconciled{Attempt: d.Seq, Outcome: o})
			wantUnchanged(t, "no answer", d, d2, cmds)
		}
		d2, cmds := Step(d, Reconciled{Attempt: 1, Outcome: ReconcileAbsent})
		wantUnchanged(t, "late answer", d, d2, cmds)
	})
	t.Run("Reconciled outside Submitting", func(t *testing.T) {
		for _, name := range []string{"idle", "preparing", "ready", "completed", "refused", "cancelled"} {
			d := to(k, name)
			d2, cmds := Step(d, Reconciled{Attempt: d.Seq, Outcome: ReconcileAbsent})
			wantUnchanged(t, name, d, d2, cmds)
		}
	})

	t.Run("Cancel before submission", func(t *testing.T) {
		d, cmds := Step(to(k, "idle"), Cancel{})
		wantState[Cancelled](t, "idle", d)
		wantCmds(t, "idle", cmds)
		d, cmds = Step(to(k, "preparing"), Cancel{})
		wantState[Cancelled](t, "preparing", d)
		wantCmds(t, "preparing", cmds, "CancelPreparation", "Audit", "Publish")
		d, cmds = Step(to(k, "ready"), Cancel{})
		wantState[Cancelled](t, "ready", d)
		wantCmds(t, "ready", cmds, "CancelPreparation", "Audit", "Publish")
	})
	t.Run("Cancel after submission changes nothing", func(t *testing.T) {
		for _, name := range []string{"submitting", "completed", "refused", "cancelled"} {
			d := to(k, name)
			d2, cmds := Step(d, Cancel{})
			wantUnchanged(t, name, d, d2, cmds)
		}
	})

	t.Run("a reused RequestID", func(t *testing.T) {
		for _, name := range []string{"preparing", "ready", "submitting", "completed", "refused", "cancelled"} {
			d := to(k, name)
			// Same request: answered from the stored state, nothing redone.
			d2, cmds := Step(d, Requested{Request: reqFor(k), IDs: IDs{Task: "tsk_2"}})
			wantUnchanged(t, name+" replay", d, d2, cmds)
			// Same key, another body: rejected, the stored request stays.
			other := reqFor(k)
			other.SourceID = "78"
			d2, cmds = Step(d, Requested{Request: other, IDs: idsFor(k)})
			if !reflect.DeepEqual(d, d2) {
				t.Errorf("%s: a changed digest touched the stored request", name)
			}
			wantCmds(t, name+" mismatch", cmds, "Explain")
			if e := cmds[0].(Explain); e.Reason != RefuseDigestMismatch {
				t.Errorf("reason %s", e.Reason)
			}
			// Another key on this submission is not ours.
			foreign := reqFor(k)
			foreign.RequestID = "req_2"
			d2, cmds = Step(d, Requested{Request: foreign, IDs: idsFor(k)})
			wantUnchanged(t, name+" foreign", d, d2, cmds)
		}
	})
	t.Run("an input of an unknown type", func(t *testing.T) {
		d := to(k, "ready")
		d2, cmds := Step(d, nil)
		wantUnchanged(t, "nil input", d, d2, cmds)
	})
}

// A submit from a refusal or a cancel can never run: those are final.
func TestFinalStatesAreFinal(t *testing.T) {
	for _, name := range []string{"completed", "refused", "cancelled"} {
		d := to(DiagnoseRepo, name)
		for _, in := range []Input{
			Prepared{Attempt: 1, Facts: factsFor(DiagnoseRepo)}, PrepareFailed{Attempt: 1}, SubmitRequested{ScopeHead: "h1"},
			Submitted{Attempt: d.Seq}, SubmitFailed{Attempt: d.Seq}, Cancel{}, Recovered{}, Reconciled{Attempt: d.Seq},
		} {
			d2, cmds := Step(d, in)
			wantUnchanged(t, name, d, d2, cmds)
		}
	}
}

// An issue import completes with a draft: its dispatch is a draft effect, not a
// queue submission, and a pull is a proposal effect.
func TestDispatchEffects(t *testing.T) {
	want := map[Kind]Effect{
		ImportIssue: EffectDraftTask, PullAndCheck: EffectStartPull,
		DiagnosePipeline: EffectSubmitTask, FixPipeline: EffectSubmitTask, ReviewMR: EffectSubmitTask, ProposeCommits: EffectSubmitTask,
		DiagnoseBuild: EffectSubmitTask, DiagnoseRepo: EffectSubmitTask, ProposeIgnore: EffectSubmitTask,
	}
	if len(want) != len(Kinds()) {
		t.Fatal("the table misses a kind")
	}
	for _, k := range Kinds() {
		if got := k.Effect(); got != want[k] {
			t.Errorf("%s effect = %q, want %q", k, got, want[k])
		}
		d := Dispatch{Request: reqFor(k), Context: PreparedContext{WaitingForFolder: true}}
		wantEff := want[k]
		if wantEff == EffectSubmitTask {
			wantEff = EffectDraftTask // an unreadable folder only ever drafts
		}
		if got := d.Effect(); got != wantEff {
			t.Errorf("%s waiting for its folder: %q, want %q", k, got, wantEff)
		}
	}
	if Kind("x").Effect() != "" {
		t.Error("an unknown kind has an effect")
	}
}
