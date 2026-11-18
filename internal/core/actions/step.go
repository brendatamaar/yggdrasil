package actions

import (
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxProposals bounds the proposals one request may create.
const MaxProposals = 32

// Effect says what a Dispatch does once the shell interprets it. The set is
// closed and holds no repair of any kind: nothing here resets, merges, removes
// or force-pushes (invariant 1; TestPropNoUnsafeRepairCommandExists).
type Effect string

const (
	// EffectSubmitTask submits one Ask or Code task with the context attached.
	EffectSubmitTask Effect = "submit_task"
	// EffectDraftTask stores a draft task only: no process and no worktree. It
	// is what an issue import does, and what any task does while its repo
	// folder cannot be read.
	EffectDraftTask Effect = "draft_task"
	// EffectStartPull creates one confirmed fast-forward pull proposal through
	// the existing git service.
	EffectStartPull Effect = "start_pull"
)

// Effect is the effect a kind has when it is dispatched.
func (k Kind) Effect() Effect {
	switch k {
	case ImportIssue:
		return EffectDraftTask
	case PullAndCheck:
		return EffectStartPull
	case DiagnosePipeline, FixPipeline, ReviewMR, ProposeCommits, DiagnoseBuild, DiagnoseRepo, ProposeIgnore:
		return EffectSubmitTask
	}
	return ""
}

// Effect is what this dispatch does. A task whose folder cannot be read is only
// ever a draft: no agent run is fabricated for a filesystem that is missing.
func (d Dispatch) Effect() Effect {
	e := d.Request.Kind.Effect()
	if e == EffectSubmitTask && d.Context.WaitingForFolder {
		return EffectDraftTask
	}
	return e
}

// Step is (submission2, commands) = Step(submission1, input) following the table
// of docs/design/phase-6-actions.md. An input that does not apply returns the
// submission unchanged and no commands. Results are fenced by attempt: a late
// result of an old attempt changes nothing and runs nothing. At most one
// Dispatch is ever emitted per submission.
func Step(d Submission, in Input) (Submission, []Command) {
	switch in := in.(type) {
	case Requested:
		return onRequested(d, in)
	case Prepared:
		return onPrepared(d, in)
	case PrepareFailed:
		return onPrepareFailed(d, in)
	case SubmitRequested:
		return onSubmitRequested(d, in)
	case Submitted:
		return onResult(d, in.Attempt, in.Result)
	case SubmitFailed:
		return onSubmitFailed(d, in)
	case Cancel:
		return onCancel(d)
	case Recovered:
		return onRecovered(d)
	case Reconciled:
		return onReconciled(d, in)
	}
	return d, nil
}

func stateOf(d Submission) State {
	if d.State == nil {
		return Idle{}
	}
	return d.State
}

func (d Submission) moved(s State) Submission {
	d.State = s
	d.Version++
	return d
}

func (d Submission) audit(action, outcome string) Audit {
	return Audit{Action: action, Request: d.Request.RequestID, Kind: d.Request.Kind, Outcome: outcome}
}

// refuse moves to Refused and explains. The refusal is audited and published
// like every state change.
func refuse(d Submission, why RefuseReason, before ...Command) (Submission, []Command) {
	d = d.moved(Refused{Reason: why})
	cmds := append(before, Explain{Reason: why}, d.audit("action.refused", string(why)), Publish{Request: d.Request.RequestID})
	return d, cmds
}

func idsFit(k Kind, ids IDs) bool {
	switch k.Effect() {
	case EffectSubmitTask, EffectDraftTask:
		return ids.Task.Validate() == nil
	case EffectStartPull:
		return proposalsFit(ids.Proposals)
	}
	return false
}

func proposalsFit(ps []core.ProposalID) bool {
	if len(ps) < 1 || len(ps) > MaxProposals {
		return false
	}
	seen := map[core.ProposalID]bool{}
	for _, p := range ps {
		if p.Validate() != nil || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// resultFits reports whether a result has the shape its kind produces: a task
// or draft id for the task kinds, proposals for a pull. Anything else is an
// impossible record, which never completes a submission.
func resultFits(k Kind, r Result) bool {
	switch k.Effect() {
	case EffectSubmitTask, EffectDraftTask:
		return r.TaskID.Validate() == nil && len(r.ProposalIDs) == 0
	case EffectStartPull:
		return r.TaskID == "" && proposalsFit(r.ProposalIDs)
	}
	return false
}

func onRequested(d Submission, in Requested) (Submission, []Command) {
	if d.Digest != "" {
		// The key is already in use. The same request is a replay: the stored
		// state answers it and nothing is redispatched. A changed request under
		// the same key is rejected without touching the stored one.
		if in.Request.RequestID != d.Request.RequestID {
			return d, nil
		}
		if in.Request.Digest() != d.Digest {
			return d, []Command{Explain{Reason: RefuseDigestMismatch}}
		}
		return d, nil
	}
	if _, idle := stateOf(d).(Idle); !idle {
		return d, nil
	}
	d.Request, d.Digest, d.IDs = in.Request, in.Request.Digest(), in.IDs
	if in.Request.Validate() != nil || !idsFit(in.Request.Kind, in.IDs) {
		return refuse(d, RefuseInvalid)
	}
	d.Seq = 1
	d = d.moved(Preparing{Attempt: d.Seq})
	return d, []Command{
		d.audit("action.requested", "preparing"),
		LoadFactsAndContext{Attempt: d.Seq, Request: d.Request},
	}
}

// Admit decides whether the facts still offer the requested action, and returns
// the offering it is built from. It is the scope re-check of the design: a
// different repo, a source that changed or an action no longer offered refuses.
func Admit(f Facts, r Request) (Offering, RefuseReason, bool) {
	if f.Repo != r.RepoID {
		return Offering{}, RefuseScopeChanged, false
	}
	if r.Kind == ImportIssue {
		if !f.Caps.HostIssues {
			return Offering{}, RefuseCapability, false
		}
		return Offering{Kind: ImportIssue, Mode: r.Kind.Mode(), Context: ContextNone, SourceID: r.SourceID}, "", true
	}
	sameKind := false
	for _, o := range Catalogue(f) {
		if o.Kind != r.Kind {
			continue
		}
		if o.SourceID == r.SourceID {
			return o, "", true
		}
		sameKind = true
	}
	if sameKind {
		return Offering{}, RefuseScopeChanged, false
	}
	return Offering{}, RefuseNoOffering, false
}

// StillCurrent reports whether the pin a preview showed (expected) is still the
// pin of the facts now. It is the one stale-head rule: the machine applies it at
// submit and the shell applies it again right before the effect, so no copy of
// it exists outside the core. An empty pin matches only an empty pin: a repo
// whose head could not be read is never taken to match one that could.
func StillCurrent(expected, now string) bool { return expected == now }

// ModeAllowed reports whether a task of this kind may be submitted in this mode.
// Ask is the mode of every kind that submits a task; Code is only ever asked
// for by FixPipeline, as a new task in its own worktree (AC-03, invariant 2).
// The mode of a kind that starts no task (a draft import, a pull) is never
// allowed, and neither is any other mode.
func ModeAllowed(k Kind, m core.TaskMode) bool {
	switch m {
	case core.ModeAsk:
		return k.Effect() == EffectSubmitTask
	case core.ModeCode:
		return k == FixPipeline
	}
	return false
}

// clampText cuts s to at most max bytes on a rune boundary.
func clampText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// prepared turns what the shell loaded into the context a preview shows: bounded
// text, an availability that never claims more than the offering has, labelled
// gaps for anything missing and the folder flag the facts decided.
func prepared(c PreparedContext, o Offering) PreparedContext {
	var cut bool
	c.Text, cut = clampText(c.Text, MaxContextBytes)
	c.Truncated = c.Truncated || cut
	switch {
	case o.Context == ContextNone:
		c.Availability = ContextNone
	case o.Context == ContextMetadataOnly || c.Availability != ContextAvailable:
		c.Availability = ContextMetadataOnly
	default:
		c.Availability = ContextAvailable
	}
	c.Unavailable = slices.Clone(c.Unavailable)
	if c.Availability == ContextMetadataOnly && len(c.Unavailable) == 0 {
		if o.Note != "" {
			c.Unavailable = []string{o.Note}
		} else {
			c.Unavailable = []string{"The source detail could not be read."}
		}
	}
	c.WaitingForFolder = c.WaitingForFolder || o.WaitingForFolder
	return c
}

func onPrepared(d Submission, in Prepared) (Submission, []Command) {
	p, ok := stateOf(d).(Preparing)
	if !ok || p.Attempt != in.Attempt {
		return d, nil
	}
	o, why, ok := Admit(in.Facts, d.Request)
	if !ok {
		return refuse(d, why)
	}
	ctx := prepared(in.Context, o)
	d = d.moved(Ready{Context: ctx, ExpectedHead: Pin(in.Facts, d.Request.Kind)})
	return d, []Command{PresentPreview{Context: ctx}}
}

func onPrepareFailed(d Submission, in PrepareFailed) (Submission, []Command) {
	p, ok := stateOf(d).(Preparing)
	if !ok || p.Attempt != in.Attempt {
		return d, nil
	}
	why := in.Reason
	if why.Validate() != nil {
		why = RefusePrepareFailed
	}
	return refuse(d, why)
}

// reconcile looks up the durable link of the attempt in flight. Submitting and
// ReconcileDispatch carry the same two fields.
func reconcile(st Submitting) Command { return ReconcileDispatch(st) }

func attemptKey(id core.RequestID, attempt int) string {
	return "action:" + string(id) + ":" + strconv.Itoa(attempt)
}

func onSubmitRequested(d Submission, in SubmitRequested) (Submission, []Command) {
	switch st := stateOf(d).(type) {
	case Ready:
		if !StillCurrent(st.ExpectedHead, in.ScopeHead) {
			return refuse(d, RefuseStaleHead)
		}
		d.Seq++
		key := attemptKey(d.Request.RequestID, d.Seq)
		ctx, ids := st.Context, d.IDs
		d = d.moved(Submitting{Attempt: d.Seq, Key: key})
		return d, []Command{
			PersistIntent{Request: d.Request, Digest: d.Digest, Attempt: d.Seq, Key: key},
			d.audit("action.submitted", "submitting"),
			Dispatch{Attempt: d.Seq, Key: key, Request: d.Request, Context: ctx, IDs: ids},
		}
	case Submitting:
		// A double click or a retry of an uncertain submission looks the first
		// one up; it never starts another.
		return d, []Command{reconcile(st)}
	case Idle, Preparing, Completed, Refused, Cancelled:
		return d, nil
	}
	return d, nil
}

// onResult completes a submission with a result for the attempt in flight. A
// result of another shape is an impossible record: no effect runs, the durable
// link is looked up again.
func onResult(d Submission, attempt int, r Result) (Submission, []Command) {
	st, ok := stateOf(d).(Submitting)
	if !ok || st.Attempt != attempt {
		return d, nil
	}
	if !resultFits(d.Request.Kind, r) {
		return d, []Command{reconcile(st)}
	}
	d = d.moved(Completed{Result: r})
	return d, []Command{
		PersistResult{Attempt: attempt, Result: r},
		d.audit("action.completed", "ok"),
		Publish{Request: d.Request.RequestID},
	}
}

func onSubmitFailed(d Submission, in SubmitFailed) (Submission, []Command) {
	st, ok := stateOf(d).(Submitting)
	if !ok || st.Attempt != in.Attempt {
		return d, nil
	}
	if in.Outcome != SubmitDefiniteRefusal {
		// Unknown (or anything unrecognised): the effect may exist.
		return d, []Command{reconcile(st)}
	}
	why := in.Reason
	if why.Validate() != nil {
		why = RefuseAdmission
	}
	return refuse(d, why, PersistResult{Attempt: st.Attempt})
}

func onCancel(d Submission) (Submission, []Command) {
	switch st := stateOf(d).(type) {
	case Idle:
		return d.moved(Cancelled{}), nil
	case Preparing:
		d = d.moved(Cancelled{})
		return d, []Command{CancelPreparation(st), d.audit("action.cancelled", "preparing"), Publish{Request: d.Request.RequestID}}
	case Ready:
		d = d.moved(Cancelled{})
		return d, []Command{CancelPreparation{Attempt: d.Seq}, d.audit("action.cancelled", "ready"), Publish{Request: d.Request.RequestID}}
	case Submitting, Completed, Refused, Cancelled:
		// After submission the admitted task is cancelled through the task
		// endpoint; this machine cannot roll it back.
		return d, nil
	}
	return d, nil
}

func onRecovered(d Submission) (Submission, []Command) {
	if st, ok := stateOf(d).(Submitting); ok {
		return d, []Command{reconcile(st)}
	}
	return d, nil
}

func onReconciled(d Submission, in Reconciled) (Submission, []Command) {
	st, ok := stateOf(d).(Submitting)
	if !ok || st.Attempt != in.Attempt {
		return d, nil
	}
	switch in.Outcome {
	case ReconcileFound:
		if !resultFits(d.Request.Kind, in.Result) {
			return d, nil // impossible record: stop, no effects
		}
		return onResult(d, in.Attempt, in.Result)
	case ReconcileAbsent:
		return refuse(d, RefuseAbsentAfterLoss)
	case ReconcileNoAnswer:
		return d, nil
	}
	return d, nil
}
