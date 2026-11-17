package actions

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// TestPhase6NeedsAttentionActions is exit criterion E6 at shell level: every
// attention reason and combination has an agent action that dispatches to the
// right mode, context and proposal, and degrades to a usable fallback when the
// token, the host or the folder is not available.
func TestPhase6NeedsAttentionActions(t *testing.T) {
	type want struct {
		kind    coreactions.Kind
		mode    core.TaskMode // "" for kinds that start no turn
		task    bool          // a task or draft is the result
		prop    bool          // a pull proposal is the result
		inText  []string      // appear in the attached prompt
		draft   bool          // saved as a waiting draft (no run)
		refused coreactions.RefuseReason
	}
	cases := []struct {
		name  string
		setup func(e *env)
		noDr  bool // no draft store wired
		want  []want
	}{
		{
			name: "unreadable folder keeps a waiting draft",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) {
					f.Readable = false
					f.Reasons = []coreactions.Reason{coreactions.ReasonUnreadable}
				})
			},
			want: []want{{kind: coreactions.DiagnoseRepo, task: true, draft: true, inText: []string{"cannot be read", "waits until the folder is restored"}}},
		},
		{
			name: "unreadable folder without a draft store is refused, never faked",
			noDr: true,
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) {
					f.Readable = false
					f.Reasons = []coreactions.Reason{coreactions.ReasonUnreadable}
				})
			},
			want: []want{{kind: coreactions.DiagnoseRepo, refused: coreactions.RefuseAdmission}},
		},
		{
			name: "conflicts",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonConflicts} })
			},
			want: []want{{kind: coreactions.DiagnoseRepo, mode: core.ModeAsk, task: true, inText: []string{"conflicts", "Never reset"}}},
		},
		{
			name: "diverged",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
			},
			want: []want{{kind: coreactions.DiagnoseRepo, mode: core.ModeAsk, task: true, inText: []string{"diverged"}}},
		},
		{
			name:  "failed CI with a readable log",
			setup: func(e *env) { e.ciFacts("FAIL TestCheckout expected 3 got 4") },
			want: []want{
				{kind: coreactions.DiagnosePipeline, mode: core.ModeAsk, task: true, inText: []string{"TestCheckout expected 3 got 4", "9001"}},
				{kind: coreactions.FixPipeline, mode: core.ModeCode, task: true, inText: []string{"TestCheckout expected 3 got 4"}},
			},
		},
		{
			name: "failed CI without a token is a labelled metadata-only diagnosis",
			setup: func(e *env) {
				e.ciFacts("unused")
				e.scope.set(func(f *coreactions.Facts) { f.Caps.HostLogs = false })
			},
			want: []want{
				{kind: coreactions.DiagnosePipeline, mode: core.ModeAsk, task: true, inText: []string{"Not available", "no linked host or token", "9001"}},
				{kind: coreactions.FixPipeline, mode: core.ModeCode, task: true, inText: []string{"Not available"}},
			},
		},
		{
			name: "failed CI where the host blocks the read",
			setup: func(e *env) {
				e.ciFacts("unused")
				e.ctxr.host[corehosts.ContextFailedJobs] = hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextForbidden}}
			},
			want: []want{
				{kind: coreactions.DiagnosePipeline, mode: core.ModeAsk, task: true, inText: []string{"token was refused"}},
				{kind: coreactions.FixPipeline, mode: core.ModeCode, task: true, inText: []string{"token was refused"}},
			},
		},
		{
			name: "failed CI where the host call fails",
			setup: func(e *env) {
				e.ciFacts("unused")
				e.ctxr.hostErr = fmt.Errorf("connection reset")
			},
			want: []want{
				{kind: coreactions.DiagnosePipeline, mode: core.ModeAsk, task: true, inText: []string{"could not be read right now"}},
				{kind: coreactions.FixPipeline, mode: core.ModeCode, task: true},
			},
		},
		{
			name: "failed build with its log",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) {
					f.Reasons, f.BuildID, f.Caps.BuildLog = []coreactions.Reason{coreactions.ReasonFailedBuild}, "bld_77", true
				})
				e.ctxr.build = "error: cannot find symbol Cart"
			},
			want: []want{{kind: coreactions.DiagnoseBuild, mode: core.ModeAsk, task: true, inText: []string{"cannot find symbol Cart", "bld_77"}}},
		},
		{
			name: "failed build with an unreadable log",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) {
					f.Reasons, f.BuildID, f.Caps.BuildLog = []coreactions.Reason{coreactions.ReasonFailedBuild}, "bld_77", true
				})
				e.ctxr.buildErr = fmt.Errorf("log expired")
			},
			want: []want{{kind: coreactions.DiagnoseBuild, mode: core.ModeAsk, task: true, inText: []string{"build log could not be read"}}},
		},
		{
			name: "behind is a pull proposal and no agent run",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
			},
			want: []want{{kind: coreactions.PullAndCheck, prop: true}},
		},
		{
			name: "dirty groups the exact current diff",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDirty} })
				e.ctxr.diff = DiffContext{Head: head40, DiffHash: strings.Repeat("d", 64), Text: "+func Cart() {}", Paths: []string{"cart.go", "cart_test.go"}}
			},
			want: []want{{kind: coreactions.ProposeCommits, mode: core.ModeAsk, task: true, inText: []string{"cart.go", "cart_test.go", strings.Repeat("d", 64), "never stage everything"}}},
		},
		{
			name: "dirty but unreadable is a waiting draft",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) {
					f.Readable = false
					f.Reasons = []coreactions.Reason{coreactions.ReasonDirty}
				})
			},
			want: []want{{kind: coreactions.ProposeCommits, task: true, draft: true}},
		},
		{
			name: "combination: failed CI, dirty, behind and conflicts",
			setup: func(e *env) {
				e.ciFacts("boom")
				e.scope.set(func(f *coreactions.Facts) {
					f.Reasons = []coreactions.Reason{coreactions.ReasonFailedCI, coreactions.ReasonDirty, coreactions.ReasonBehind, coreactions.ReasonConflicts}
				})
				e.ctxr.diff = DiffContext{Head: head40, DiffHash: strings.Repeat("e", 64), Text: "x", Paths: []string{"a.go"}}
			},
			want: []want{
				{kind: coreactions.DiagnoseRepo, mode: core.ModeAsk, task: true},
				{kind: coreactions.DiagnosePipeline, mode: core.ModeAsk, task: true, inText: []string{"boom"}},
				{kind: coreactions.FixPipeline, mode: core.ModeCode, task: true},
				{kind: coreactions.PullAndCheck, prop: true},
				{kind: coreactions.ProposeCommits, mode: core.ModeAsk, task: true, inText: []string{"a.go"}},
			},
		},
		{
			name: "an open merge request is reviewed as an Ask task",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.MRNumber, f.MRHead, f.Caps.HostMR = 7, head40b, true })
				e.ctxr.host[corehosts.ContextMRDiff] = ready(corehosts.MRContext{MR: corehosts.MergeRequest{Number: 7, Title: "Add cart"}, Head: head40b, Diff: "+cart"})
			},
			want: []want{{kind: coreactions.ReviewMR, mode: core.ModeAsk, task: true, inText: []string{"Add cart", "+cart", head40b}}},
		},
		{
			name: "many untracked files are reviewed for ignore entries",
			setup: func(e *env) {
				e.scope.set(func(f *coreactions.Facts) { f.UntrackedCount = 400 })
				var sample []string
				for i := 0; i < 400; i++ {
					sample = append(sample, fmt.Sprintf("node_modules/pkg%d/index.js", i))
				}
				e.scope.noise.Repo, e.scope.noise.UntrackedCount, e.scope.noise.Sample = e.repo.ID, 400, sample
			},
			want: []want{{kind: coreactions.ProposeIgnore, mode: core.ModeAsk, task: true, inText: []string{"/node_modules/"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.noDr {
				e.svc.d.Drafts = nil
			}
			c.setup(e)
			cat, err := e.svc.Offerings(context.Background(), Scope{Repo: e.repo.ID})
			if err != nil {
				t.Fatal(err)
			}
			offs := cat.Repos[0].Offerings
			if len(offs) < len(c.want) {
				t.Fatalf("catalogue has %d offerings, want at least %d: %+v", len(offs), len(c.want), offs)
			}
			for i, w := range c.want {
				var off *coreactions.Offering
				for j := range offs {
					if offs[j].Kind == w.kind {
						off = &offs[j]
						break
					}
				}
				if off == nil {
					t.Fatalf("no %s offering in %+v", w.kind, offs)
				}
				id := fmt.Sprintf("req_%d", i)
				callsBefore := len(e.tasks.requests)
				out := e.dispatch(id, w.kind, off.SourceID)
				switch {
				case w.refused != "":
					if out.State != db.ActionRefused || out.Refusal != w.refused || out.Message == "" {
						t.Fatalf("%s: %+v, want refused %s with a message", w.kind, out, w.refused)
					}
					if e.tasks.taskCount() != 0 {
						t.Fatalf("%s: a task exists although nothing may run", w.kind)
					}
					continue
				case out.State != db.ActionCompleted:
					t.Fatalf("%s: %+v, want completed", w.kind, out)
				}
				if w.task && out.Result.TaskID == "" || w.prop && len(out.Result.ProposalIDs) != 1 {
					t.Fatalf("%s: result %+v", w.kind, out.Result)
				}
				var prompt string
				switch {
				case w.draft:
					if e.drafts.calls == 0 || e.tasks.agentCalls() != 0 {
						t.Fatalf("%s: a waiting draft must start nothing (draft calls %d, agent calls %d)", w.kind, e.drafts.calls, e.tasks.agentCalls())
					}
					prompt = e.drafts.last.Brief
				case w.task:
					if len(e.tasks.requests) != callsBefore+1 {
						t.Fatalf("%s: %d task submissions, want 1", w.kind, len(e.tasks.requests)-callsBefore)
					}
					req := e.tasks.requests[len(e.tasks.requests)-1]
					if core.TaskMode(req.Mode) != w.mode || req.Scope != string(core.ScopeRepo) || len(req.Repos) != 1 || req.Repos[0] != string(e.repo.ID) {
						t.Fatalf("%s: submitted %+v, want mode %s on the repo only", w.kind, req, w.mode)
					}
					prompt = req.Prompt
				case w.prop:
					if len(e.props.specs) == 0 {
						t.Fatal("no proposal was created")
					}
					spec := e.props.specs[len(e.props.specs)-1]
					a, perr := coreproposals.ActionFromRow(spec.Kind, spec.Payload)
					pull, ok := a.(coreproposals.PullFF)
					if perr != nil || !ok || pull.Upstream != "origin/main" || pull.ExpectHead != head40 {
						t.Fatalf("proposal %v %v is not the expected PullFF", a, perr)
					}
				}
				for _, s := range w.inText {
					if !strings.Contains(prompt, s) {
						t.Errorf("%s: prompt lacks %q:\n%s", w.kind, s, prompt)
					}
				}
			}
		})
	}
}

// Every reason has a dispatchable action: a property over all reasons and all
// pairs of reasons, at shell level.
func TestEveryReasonAndPairHasADispatchableAction(t *testing.T) {
	reasons := coreactions.Reasons()
	for i := range reasons {
		for j := i; j < len(reasons); j++ {
			e := newEnv(t)
			e.ciFacts("x")
			e.scope.set(func(f *coreactions.Facts) {
				f.Reasons = []coreactions.Reason{reasons[i], reasons[j]}
				f.BuildID, f.Caps.BuildLog = "bld_1", true
			})
			e.ctxr.diff = DiffContext{Head: head40, DiffHash: strings.Repeat("d", 64), Text: "x", Paths: []string{"a.go"}}
			cat, err := e.svc.Offerings(context.Background(), Scope{Repo: e.repo.ID})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range []coreactions.Reason{reasons[i], reasons[j]} {
				k, _ := coreactions.PrimaryKind(r)
				var found *coreactions.Offering
				for x := range cat.Repos[0].Offerings {
					if cat.Repos[0].Offerings[x].Kind == k {
						found = &cat.Repos[0].Offerings[x]
					}
				}
				if found == nil {
					t.Fatalf("reasons %v: %s has no %s action", []coreactions.Reason{reasons[i], reasons[j]}, r, k)
				}
				out := e.dispatch(fmt.Sprintf("req_%s_%s_%s", reasons[i], reasons[j], k), k, found.SourceID)
				if out.State != db.ActionCompleted {
					t.Fatalf("%s: %+v", k, out)
				}
			}
		}
	}
}
