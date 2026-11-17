package actions

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var epoch = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// reqFor is a valid request of a kind, with the source the facts of factsFor
// offer.
func reqFor(k Kind) Request {
	src := map[Kind]string{DiagnosePipeline: "77", FixPipeline: "77", ReviewMR: "12", ImportIssue: "5", DiagnoseBuild: "88"}[k]
	return Request{RequestID: "req_1", RepoID: "rep_1", Kind: k, SourceID: src, Agent: "mock"}
}

func idsFor(k Kind) IDs {
	if k.Effect() == EffectStartPull {
		return IDs{Proposals: []core.ProposalID{"prp_1"}}
	}
	return IDs{Task: "tsk_1"}
}

// factsFor are facts under which the catalogue offers kind k with reqFor(k)'s
// source: everything readable and every capability present.
func factsFor(k Kind) Facts {
	f := Facts{
		Repo: "rep_1", Readable: true, Caps: capsOf(31), CIRunID: "77", CIHead: "c1", BuildID: "88",
		MRNumber: 12, MRHead: "m1", UntrackedCount: 200, Head: "h1",
	}
	switch k {
	case DiagnosePipeline, FixPipeline:
		f.Reasons = []Reason{ReasonFailedCI}
	case DiagnoseBuild:
		f.Reasons = []Reason{ReasonFailedBuild}
	case DiagnoseRepo:
		f.Reasons = []Reason{ReasonConflicts}
	case ProposeCommits, ProposeIgnore:
		f.Reasons = []Reason{ReasonDirty}
	case PullAndCheck:
		f.Reasons = []Reason{ReasonBehind}
	}
	return f
}

func resultFor(k Kind) Result {
	if k.Effect() == EffectStartPull {
		return Result{ProposalIDs: []core.ProposalID{"prp_1"}}
	}
	return Result{TaskID: "tsk_1"}
}

func readyCtx() PreparedContext {
	return PreparedContext{Source: "CI run 77 on shop-api", Text: "log", Availability: ContextAvailable}
}

// to drives a fresh submission of kind k up to the wanted state name, with the
// facts and context of factsFor.
func to(k Kind, name string) Submission {
	d := Submission{State: Idle{}}
	if name == "idle" {
		return d
	}
	d, _ = Step(d, Requested{Request: reqFor(k), IDs: idsFor(k), Now: epoch})
	if name == "preparing" {
		return d
	}
	if name == "cancelled" {
		d, _ = Step(d, Cancel{})
		return d
	}
	if name == "prepare_refused" {
		d, _ = Step(d, PrepareFailed{Attempt: 1, Reason: RefusePrepareFailed})
		return d
	}
	d, _ = Step(d, Prepared{Attempt: 1, Facts: factsFor(k), Context: readyCtx()})
	if name == "ready" {
		return d
	}
	d, _ = Step(d, SubmitRequested{ScopeHead: Pin(factsFor(k), k), Now: epoch})
	if name == "submitting" {
		return d
	}
	if name == "completed" {
		d, _ = Step(d, Submitted{Attempt: d.Seq, Result: resultFor(k)})
		return d
	}
	if name == "refused" {
		d, _ = Step(d, SubmitFailed{Attempt: d.Seq, Outcome: SubmitDefiniteRefusal, Reason: RefuseAdmission})
		return d
	}
	panic("unknown state " + name)
}
