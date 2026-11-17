package actions

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Property tests of the dispatch machine: random input sequences with the
// invariants checked after every step. The model keeps its own history of what
// was fed; it never reads the machine to decide what an invariant demands.

const (
	propRuns  = 600
	propSteps = 40
)

type propModel struct {
	rnd  *rand.Rand
	d    Submission
	only string // when set, only violations tagged with it count

	firstReq   *Request // the first request fed while the submission was fresh
	dispatches int
	lastFacts  Facts // facts of the most recent Prepared input
	readyPin   string
	readyFacts Facts
	haveReady  bool
	violation  string
}

func (m *propModel) fail(f string, a ...any) {
	if m.only != "" && !strings.HasPrefix(f, m.only+":") {
		return
	}
	if m.violation == "" {
		m.violation = fmt.Sprintf(f, a...)
	}
}

func stateName(s State) string {
	if s == nil {
		return "Idle"
	}
	return strings.TrimPrefix(reflect.TypeOf(s).String(), "actions.")
}

func isFinal(s State) bool {
	switch s.(type) {
	case Completed, Refused, Cancelled:
		return true
	case nil, Idle, Preparing, Ready, Submitting:
		return false
	}
	return false
}

func (m *propModel) pick(xs ...int) int { return xs[m.rnd.IntN(len(xs))] }

func (m *propModel) attempt() int {
	return m.pick(m.d.Seq, m.d.Seq, m.d.Seq, m.d.Seq-1, m.d.Seq+1, 0)
}

func (m *propModel) kind() Kind {
	if m.d.Request.Kind != "" && m.rnd.IntN(8) != 0 {
		return m.d.Request.Kind
	}
	return Kinds()[m.rnd.IntN(len(Kinds()))]
}

func (m *propModel) facts(k Kind) Facts {
	f := factsFor(k)
	switch m.rnd.IntN(12) {
	case 0:
		f.Repo = "rep_2"
	case 1:
		f.Readable = false
	case 2:
		f.Caps = capsOf(m.rnd.IntN(32))
	case 3:
		f.CIRunID, f.BuildID = "99", "99"
	case 4:
		f.Reasons = nil
	case 5:
		f.Head, f.CIHead = "h2", "c2"
	}
	return f
}

func (m *propModel) context() PreparedContext {
	c := PreparedContext{Source: "src", Availability: []ContextAvailability{ContextAvailable, ContextMetadataOnly, ContextNone, ""}[m.rnd.IntN(4)]}
	switch m.rnd.IntN(4) {
	case 0:
		c.Text = strings.Repeat("x", MaxContextBytes+m.rnd.IntN(50))
	case 1:
		c.Text = "log"
	}
	return c
}

func (m *propModel) result(k Kind) Result {
	switch m.rnd.IntN(8) {
	case 0:
		return Result{}
	case 1:
		other := Kinds()[m.rnd.IntN(len(Kinds()))]
		return resultFor(other)
	}
	return resultFor(k)
}

func (m *propModel) input() Input {
	k := m.kind()
	bias := m.rnd.IntN(10) < 6
	switch normState(m.d.State).(type) {
	case Idle:
		if bias {
			return m.requested()
		}
	case Preparing:
		if bias {
			return Prepared{Attempt: m.d.Seq, Facts: m.facts(m.d.Request.Kind), Context: m.context()}
		}
	case Ready:
		if bias {
			return SubmitRequested{ScopeHead: m.scopeHead(), Now: epoch}
		}
	case Completed, Refused, Cancelled:
		// final: only random inputs below
	case Submitting:
		if bias {
			switch m.rnd.IntN(4) {
			case 0:
				return Submitted{Attempt: m.attempt(), Result: m.result(k)}
			case 1:
				return SubmitFailed{Attempt: m.attempt(), Outcome: []SubmitOutcome{SubmitDefiniteRefusal, SubmitUnknown, "weird"}[m.rnd.IntN(3)], Reason: RefuseAdmission}
			case 2:
				return Reconciled{Attempt: m.attempt(), Outcome: []ReconcileOutcome{ReconcileFound, ReconcileAbsent, ReconcileNoAnswer}[m.rnd.IntN(3)], Result: m.result(k)}
			default:
				return Recovered{Now: epoch}
			}
		}
	}
	switch m.rnd.IntN(9) {
	case 0:
		return m.requested()
	case 1:
		return Prepared{Attempt: m.attempt(), Facts: m.facts(k), Context: m.context()}
	case 2:
		return PrepareFailed{Attempt: m.attempt(), Reason: []RefuseReason{RefusePrepareFailed, RefuseUntracked, "bogus"}[m.rnd.IntN(3)]}
	case 3:
		return SubmitRequested{ScopeHead: m.scopeHead(), Now: epoch}
	case 4:
		return Submitted{Attempt: m.attempt(), Result: m.result(k)}
	case 5:
		return SubmitFailed{Attempt: m.attempt(), Outcome: []SubmitOutcome{SubmitDefiniteRefusal, SubmitUnknown}[m.rnd.IntN(2)], Reason: RefuseAdmission}
	case 6:
		return Cancel{}
	case 7:
		return Recovered{Now: epoch}
	}
	return Reconciled{Attempt: m.attempt(), Outcome: []ReconcileOutcome{ReconcileFound, ReconcileAbsent, ReconcileNoAnswer}[m.rnd.IntN(3)], Result: m.result(k)}
}

func (m *propModel) requested() Input {
	k := Kinds()[m.rnd.IntN(len(Kinds()))]
	r := reqFor(k)
	switch m.rnd.IntN(10) {
	case 0:
		r.Kind = "force_push"
	case 1:
		r.Agent = "other"
	case 2:
		r.RequestID = "req_2"
	}
	ids := idsFor(k)
	if m.rnd.IntN(10) == 0 {
		ids = IDs{}
	}
	return Requested{Request: r, IDs: ids, Now: epoch}
}

func (m *propModel) scopeHead() string {
	if m.haveReady || m.lastFacts.Repo != "" {
		if m.rnd.IntN(2) == 0 {
			return Pin(m.lastFacts, m.d.Request.Kind)
		}
	}
	return []string{"", "stale", "h2", "c2"}[m.rnd.IntN(4)]
}

var knownCommand = map[string]bool{
	"LoadFactsAndContext": true, "PresentPreview": true, "Explain": true, "PersistIntent": true, "Dispatch": true,
	"PersistResult": true, "Audit": true, "Publish": true, "ReconcileDispatch": true, "CancelPreparation": true,
}

func normState(s State) State {
	if s == nil {
		return Idle{}
	}
	return s
}

func (m *propModel) apply(in Input) {
	b := m.d
	if r, ok := in.(Requested); ok && b.Digest == "" && m.firstReq == nil {
		rr := r.Request
		m.firstReq = &rr
	}
	if p, ok := in.(Prepared); ok {
		m.lastFacts = p.Facts
	}
	a, cmds := Step(b, in)
	m.d = a
	bs, as := normState(b.State), normState(a.State)
	changed := !reflect.DeepEqual(bs, as)

	// GEN: bookkeeping invariants.
	if b.Digest != "" && (a.Digest != b.Digest || a.Request != b.Request) {
		m.fail("DIGEST: the stored request or digest changed from %+v to %+v", b.Request, a.Request)
	}
	if a.Digest != "" && a.Digest != a.Request.Digest() {
		m.fail("DIGEST: digest %q does not belong to the stored request", a.Digest)
	}
	if a.Seq < b.Seq || a.Version < b.Version {
		m.fail("GEN: counters went backwards")
	}
	if changed != (a.Version == b.Version+1) || (!changed && a.Version != b.Version) {
		m.fail("GEN: version %d -> %d with state change %v", b.Version, a.Version, changed)
	}
	for _, c := range cmds {
		if !knownCommand[strings.TrimPrefix(reflect.TypeOf(c).String(), "actions.")] {
			m.fail("GEN: unknown command %T", c)
		}
	}
	// Final states stay final; nothing but the digest mismatch note leaves them.
	if isFinal(bs) {
		if changed {
			m.fail("GEN: %s changed to %s on %T", stateName(bs), stateName(as), in)
		}
		for _, c := range cmds {
			if e, ok := c.(Explain); !ok || e.Reason != RefuseDigestMismatch {
				m.fail("GEN: %s emitted %T on %T", stateName(bs), c, in)
			}
		}
	}
	if !changed {
		for _, c := range cmds {
			if _, ok := c.(ReconcileDispatch); ok {
				if _, sub := bs.(Submitting); !sub {
					m.fail("GEN: ReconcileDispatch outside Submitting")
				}
			} else if e, ok := c.(Explain); ok {
				if _, req := in.(Requested); !req || e.Reason != RefuseDigestMismatch {
					m.fail("GEN: an unchanged state explained %s", e.Reason)
				}
			} else {
				m.fail("GEN: an unchanged state emitted %T on %T", c, in)
			}
		}
	}
	// Every state change but a preview is audited; every refusal is explained.
	if changed {
		_, toReady := as.(Ready)
		_, fromIdle := bs.(Idle)
		_, toCancel := as.(Cancelled)
		if !toReady && (!fromIdle || !toCancel) && !slices.ContainsFunc(cmds, func(c Command) bool { _, ok := c.(Audit); return ok }) {
			m.fail("GEN: %s -> %s was not audited", stateName(bs), stateName(as))
		}
		if _, ok := as.(Refused); ok && !slices.ContainsFunc(cmds, func(c Command) bool { _, ok := c.(Explain); return ok }) {
			m.fail("GEN: a refusal was not explained")
		}
	}
	for _, c := range cmds {
		if au, ok := c.(Audit); ok {
			if au.Request != a.Request.RequestID || au.Kind != a.Request.Kind {
				m.fail("GEN: audit names %s/%s", au.Request, au.Kind)
			}
			for _, bad := range unsafeWords {
				if strings.Contains(strings.ToLower(au.Action+" "+au.Outcome), bad) {
					m.fail("UNSAFE: audit %q mentions %q", au.Action, bad)
				}
			}
		}
	}

	// Entering Ready: the facts offered the action and the pin is the facts'.
	if _, ok := as.(Ready); ok && changed {
		f := m.lastFacts
		r := a.Request
		offered := f.Repo == r.RepoID && (r.Kind == ImportIssue && f.Caps.HostIssues ||
			slices.ContainsFunc(Catalogue(f), func(o Offering) bool { return o.Kind == r.Kind && o.SourceID == r.SourceID }))
		if !offered {
			m.fail("STALE: Ready for %s on facts that no longer offer it: %+v", r.Kind, f)
		}
		if as.(Ready).ExpectedHead != Pin(f, r.Kind) {
			m.fail("STALE: expected head %q, pin %q", as.(Ready).ExpectedHead, Pin(f, r.Kind))
		}
		m.readyPin, m.readyFacts, m.haveReady = Pin(f, r.Kind), f, true
		if len(as.(Ready).Context.Text) > MaxContextBytes {
			m.fail("GEN: unbounded context of %d bytes", len(as.(Ready).Context.Text))
		}
	}

	// Dispatch: at most once ever, only from Ready on a pinned, current scope.
	n := 0
	for _, c := range cmds {
		d, ok := c.(Dispatch)
		if !ok {
			continue
		}
		n++
		m.dispatches++
		if m.dispatches > 1 {
			m.fail("DUP: a second Dispatch was emitted")
		}
		if _, ok := bs.(Ready); !ok {
			m.fail("DUP: Dispatch from %s", stateName(bs))
		}
		if _, ok := in.(SubmitRequested); !ok {
			m.fail("DUP: Dispatch on %T", in)
		}
		if sub, ok := as.(Submitting); !ok || sub.Key != d.Key || d.Key != attemptKey(a.Request.RequestID, d.Attempt) {
			m.fail("DUP: dispatch key %q does not match the state", d.Key)
		}
		if sr, ok := in.(SubmitRequested); ok && (!m.haveReady || sr.ScopeHead != m.readyPin) {
			m.fail("STALE: Dispatch with head %q, pinned %q", sr.ScopeHead, m.readyPin)
		}
		if !m.haveReady || m.readyFacts.Repo != d.Request.RepoID {
			m.fail("STALE: Dispatch for a repo the facts were not about")
		}
		if m.firstReq == nil || d.Request != *m.firstReq {
			m.fail("FIX: Dispatch carries a request that was never made: %+v", d.Request)
		}
		if d.Request.Kind.Mode() == core.ModeCode && (m.firstReq == nil || m.firstReq.Kind != FixPipeline) {
			m.fail("FIX: a Code dispatch without an explicit fix request")
		}
		if d.Request.Kind == FixPipeline && m.firstReq.Kind != FixPipeline {
			m.fail("FIX: a fix dispatch for a %s request", m.firstReq.Kind)
		}
		if d.Effect() == EffectSubmitTask && (d.Context.WaitingForFolder || !m.readyFacts.Readable) {
			m.fail("DRAFT: a task is submitted for an unreadable folder")
		}
		if d.Request.Kind == ImportIssue && (d.Effect() != EffectDraftTask || d.Request.Kind.Mode() != "") {
			m.fail("DRAFT: an import dispatches %s", d.Effect())
		}
		if d.Request.Kind != PullAndCheck && d.Request.Kind != ImportIssue && d.Request.Kind.Mode() == "" {
			m.fail("GEN: kind %s has no mode", d.Request.Kind)
		}
	}
	if n > 1 {
		m.fail("DUP: %d Dispatch commands in one step", n)
	}

	// Completed only from the attempt in flight, with the result that was fed.
	if c, ok := as.(Completed); ok && changed {
		sub, wasSub := bs.(Submitting)
		var att int
		var res Result
		if x, ok := in.(Submitted); ok {
			att, res = x.Attempt, x.Result
		} else if x, ok := in.(Reconciled); ok {
			if x.Outcome != ReconcileFound {
				m.fail("GEN: completed from a %s answer", x.Outcome)
			}
			att, res = x.Attempt, x.Result
		} else {
			m.fail("GEN: completed on %T", in)
		}
		if !wasSub || att != sub.Attempt || !reflect.DeepEqual(res, c.Result) {
			m.fail("DUP: completed from a late or foreign result")
		}
		if a.Request.Kind == ImportIssue && (c.Result.TaskID == "" || len(c.Result.ProposalIDs) != 0) {
			m.fail("DRAFT: an import completed with %+v", c.Result)
		}
		if a.Request.Kind == PullAndCheck && c.Result.TaskID != "" {
			m.fail("GEN: a pull completed with a task")
		}
	}
	// A stale submit refuses and never moves on.
	if sr, ok := in.(SubmitRequested); ok {
		if _, wasReady := bs.(Ready); wasReady {
			_, nowSub := as.(Submitting)
			if nowSub && sr.ScopeHead != b.State.(Ready).ExpectedHead {
				m.fail("STALE: submitted with head %q against %q", sr.ScopeHead, b.State.(Ready).ExpectedHead)
			}
		}
	}
}

func runProps(t *testing.T, only string, seed uint64) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for run := 0; run < propRuns; run++ {
		m := &propModel{rnd: rand.New(rand.NewPCG(seed, uint64(run))), only: only, d: Submission{State: Idle{}}}
		var trace []string
		for i := 0; i < propSteps; i++ {
			in := m.input()
			trace = append(trace, fmt.Sprintf("%T%+v", in, in))
			m.apply(in)
			seen[stateName(m.d.State)]++
			if m.violation != "" {
				t.Fatalf("run %d step %d: %s\ntrace:\n  %s", run, i, m.violation, strings.Join(trace, "\n  "))
			}
			if m.dispatches > 0 {
				seen["dispatched"]++
			}
		}
	}
	return seen
}

// The generator must not be vacuous: every state, a dispatch and each kind of
// request is reached many times.
func TestPropGeneratorReachesEveryState(t *testing.T) {
	seen := runProps(t, "", 1)
	for _, s := range []string{"Idle", "Preparing", "Ready", "Submitting", "Completed", "Refused", "Cancelled", "dispatched"} {
		if seen[s] < 200 {
			t.Errorf("%s reached only %d times", s, seen[s])
		}
	}
}

func TestPropDispatchInvariants(t *testing.T) { runProps(t, "", 1) }

// A double click, a retry or a replay starts at most one effect.
func TestPropDuplicateDispatchOneEffect(t *testing.T) { runProps(t, "DUP", 2) }

// A changed head, scope or source never launches.
func TestPropStaleScopeNeverLaunches(t *testing.T) { runProps(t, "STALE", 3) }

// Code mode only follows an explicit fix request.
func TestPropFixNeedsExplicitRequest(t *testing.T) { runProps(t, "FIX", 4) }

// An import is a draft; an unreadable folder only ever drafts.
func TestPropIssueImportStaysDraft(t *testing.T) { runProps(t, "DRAFT", 5) }

// The digest of the first request is never replaced.
func TestPropRequestDigestImmutable(t *testing.T) { runProps(t, "DIGEST", 6) }

// Exhaustive double submit: whatever the state, feeding SubmitRequested twice
// emits at most one Dispatch, and exactly one from a fresh Ready.
func TestDoubleClickEmitsOneDispatch(t *testing.T) {
	for _, k := range Kinds() {
		d := to(k, "ready")
		var n int
		for i := 0; i < 3; i++ {
			var cmds []Command
			d, cmds = Step(d, SubmitRequested{ScopeHead: Pin(factsFor(k), k)})
			for _, c := range cmds {
				if _, ok := c.(Dispatch); ok {
					n++
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d dispatches for 3 clicks", k, n)
		}
	}
}

// Ask is never turned into Code: from any kind other than FixPipeline, no
// sequence yields a Code dispatch (checked structurally over all kinds).
func TestAskNeverBecomesCode(t *testing.T) {
	for _, k := range Kinds() {
		d := to(k, "ready")
		_, cmds := Step(d, SubmitRequested{ScopeHead: Pin(factsFor(k), k)})
		for _, c := range cmds {
			if disp, ok := c.(Dispatch); ok {
				if (disp.Request.Kind.Mode() == core.ModeCode) != (k == FixPipeline) {
					t.Errorf("%s dispatched with mode %q", k, disp.Request.Kind.Mode())
				}
			}
		}
	}
}

var unsafeWords = []string{"reset", "merge", "delete", "remove", "force", "push", "clean", "checkout", "rebase", "stash"}

// TestPropNoUnsafeRepairCommandExists: the command set is closed and none of its
// types, fields or effects, nor any audit text the machine can emit, names a
// repair that resets, merges, removes or forces (invariant 1; the unsafe set of
// AGENTS.md section 6).
func TestPropNoUnsafeRepairCommandExists(t *testing.T) {
	runProps(t, "UNSAFE", 7)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]bool{}
	var literals []string // string literals of the machine, not of the catalogue notes
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Recv != nil && n.Name.Name == "isCommand" {
					commands[recvName(n.Recv.List[0].Type)] = true
				}
			case *ast.BasicLit:
				if name == "step.go" && n.Kind == token.STRING {
					literals = append(literals, n.Value)
				}
			}
			return true
		})
		// Field names of the command types.
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fl := range st.Fields.List {
				for _, id := range fl.Names {
					if ts.Name.Name == "Dispatch" || knownCommand[ts.Name.Name] {
						noUnsafeWord(t, "field "+ts.Name.Name+"."+id.Name, id.Name)
					}
				}
			}
			return true
		})
	}
	var names []string
	for c := range commands {
		names = append(names, c)
		noUnsafeWord(t, "command type "+c, c)
	}
	sort.Strings(names)
	var want []string
	for c := range knownCommand {
		want = append(want, c)
	}
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Errorf("the command set is %v, want exactly %v", names, want)
	}
	for _, l := range literals {
		noUnsafeWord(t, "literal "+l, l)
	}
	for _, k := range Kinds() {
		noUnsafeWord(t, "kind "+string(k), string(k))
		noUnsafeWord(t, "effect of "+string(k), string(k.Effect()))
	}
}

func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

func noUnsafeWord(t *testing.T, what, s string) {
	t.Helper()
	low := strings.ToLower(s)
	for _, w := range unsafeWords {
		if strings.Contains(low, w) {
			t.Errorf("%s names %q", what, w)
		}
	}
}
