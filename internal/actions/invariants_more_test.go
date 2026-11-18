package actions

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

func (e *env) effects() string {
	return fmt.Sprintf("agent calls %d, drafts %d, tasks %d, proposal creations %d", e.tasks.agentCalls(), e.tasks.drafts, e.tasks.taskCount(), e.props.creates)
}

// Invariant 2: nothing reaches a task, draft or proposal creator unless the
// intent was committed first. With a store whose every Commit fails, no kind
// causes an effect; the request is left without a row or refused.
func TestInvariant_2_NoEffectWithoutCommittedIntent(t *testing.T) {
	scenarios := map[coreactions.Kind]func(e *env) string{
		coreactions.DiagnosePipeline: func(e *env) string { e.ciFacts("FAIL"); return "9001" },
		coreactions.FixPipeline:      func(e *env) string { e.ciFacts("FAIL"); return "9001" },
		coreactions.DiagnoseRepo: func(e *env) string {
			e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
			return ""
		},
		coreactions.PullAndCheck: func(e *env) string {
			e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
			return ""
		},
		coreactions.ImportIssue: func(e *env) string {
			e.scope.set(func(f *coreactions.Facts) { f.Caps.HostIssues = true })
			e.ctxr.host[corehosts.ContextIssue] = ready(corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 42, Title: "Cart total is wrong"}, Body: "x"})
			return "42"
		},
	}
	for kind, arrange := range scenarios {
		t.Run(string(kind), func(t *testing.T) {
			e := newEnv(t)
			source := arrange(e)
			dead := &dyingStore{RequestStore: e.store}
			dead.kill()
			_ = e.svc.Close()
			e.svc = e.build(dead)
			t.Cleanup(func() { _ = e.svc.Close() })
			out, err := e.svc.Dispatch(context.Background(), Input{Request: e.req("req_1", kind, source)})
			if e.tasks.agentCalls() != 0 || e.tasks.drafts != 0 || e.tasks.taskCount() != 0 || e.props.creates != 0 {
				t.Fatalf("an effect ran without a committed intent: %s (outcome %+v, err %v)", e.effects(), out, err)
			}
			if out.State == db.ActionCompleted {
				t.Fatalf("completed without a committed intent: %+v", out)
			}
			if _, gerr := e.store.GetActionRequest(context.Background(), "req_1"); gerr == nil {
				t.Fatal("a request row exists although every commit failed")
			}
		})
	}
}

// A moved pin between the preview and the dispatch is refused as stale and
// starts nothing, for each kind of pin: the pipeline's commit, the MR head and
// the repo HEAD (AC-02, AC-03).
func TestInvariant_5_StalePinRefusedPerKind(t *testing.T) {
	cases := []struct {
		name    string
		kind    coreactions.Kind
		source  string
		arrange func(e *env)
		move    func(f *coreactions.Facts)
	}{
		{"pipeline commit", coreactions.DiagnosePipeline, "9001", func(e *env) { e.ciFacts("FAIL") },
			func(f *coreactions.Facts) { f.CIHead = head40b }},
		{"fix pipeline commit", coreactions.FixPipeline, "9001", func(e *env) { e.ciFacts("FAIL") },
			func(f *coreactions.Facts) { f.CIHead = head40b }},
		{"mr head", coreactions.ReviewMR, "3", func(e *env) {
			e.scope.set(func(f *coreactions.Facts) { f.MRNumber, f.MRHead, f.Caps.HostMR = 3, head40b, true })
			e.ctxr.host[corehosts.ContextMRDiff] = ready(corehosts.MRContext{MR: corehosts.MergeRequest{Number: 3}, Head: head40b, Diff: "x"})
		}, func(f *coreactions.Facts) { f.MRHead = head40 }},
		{"repo head", coreactions.DiagnoseRepo, "", func(e *env) {
			e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		}, func(f *coreactions.Facts) { f.Head = head40b }},
		{"pull head", coreactions.PullAndCheck, "", func(e *env) {
			e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
		}, func(f *coreactions.Facts) { f.Head = head40b }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.arrange(e)
			req := e.req("req_1", c.kind, c.source)
			p, err := e.svc.Preview(context.Background(), req)
			if err != nil || p.Refusal != "" || p.ExpectedHead == "" {
				t.Fatalf("preview %+v, %v", p, err)
			}
			if e.tasks.agentCalls()+e.tasks.drafts != 0 || e.props.creates != 0 {
				t.Fatalf("a preview had effects: %s", e.effects())
			}
			e.scope.set(c.move)
			out, err := e.svc.Dispatch(context.Background(), Input{Request: req, Digest: p.Digest, ExpectedHead: p.ExpectedHead})
			if err != nil || out.State != db.ActionRefused || out.Refusal != coreactions.RefuseStaleHead {
				t.Fatalf("outcome %+v, %v: want refused stale head", out, err)
			}
			if e.tasks.agentCalls() != 0 || e.tasks.drafts != 0 || e.props.creates != 0 {
				t.Fatalf("a stale pin still had effects: %s", e.effects())
			}
			// The same preview against an unchanged state goes through.
			e2 := newEnv(t)
			c.arrange(e2)
			req2 := e2.req("req_1", c.kind, c.source)
			p2, _ := e2.svc.Preview(context.Background(), req2)
			if out, err := e2.svc.Dispatch(context.Background(), Input{Request: req2, Digest: p2.Digest, ExpectedHead: p2.ExpectedHead}); err != nil || out.State != db.ActionCompleted {
				t.Fatalf("the unchanged state was refused: %+v, %v", out, err)
			}
		})
	}
}

// Invariant 4: tokens never reach the audit log, the stored record, the
// outcome shown, the published messages or the prompt, even when the host log
// or an admission message carries one.
func TestInvariant_4_ActionsHoldNoToken(t *testing.T) {
	leaks := []string{secret, token}
	e := newEnv(t)
	e.ciFacts("deploy failed using " + token + " and " + secret)
	diag := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
	e.tasks.failBefore = &tasks.RejectedError{Reason: "repo_untrusted", Message: "Trust this repo first (" + token + ")."}
	fix := e.dispatch("req_2", coreactions.FixPipeline, "9001")
	e.tasks.failBefore = nil
	if diag.State != db.ActionCompleted || fix.State != db.ActionRefused {
		t.Fatalf("diag %+v, fix %+v", diag, fix)
	}
	e.hub.mu.Lock()
	hubText := fmt.Sprintf("%+v", e.hub.msgs)
	e.hub.mu.Unlock()
	var prompts []string
	for _, r := range e.tasks.requests {
		prompts = append(prompts, r.Prompt, r.Title)
	}
	surfaces := map[string]string{
		"audit":      e.auditText(),
		"outcomes":   fmt.Sprintf("%+v %+v", diag, fix),
		"record 1":   fmt.Sprintf("%+v", e.record("req_1")),
		"record 2":   fmt.Sprintf("%+v", e.record("req_2")),
		"hub":        hubText,
		"prompt":     strings.Join(prompts, "\n"),
		"get":        fmt.Sprintf("%+v", mustGet(t, e, "req_2")),
		"preview":    fmt.Sprintf("%+v", mustPreview(t, e)),
		"audit ords": fmt.Sprint(e.auditActions()),
	}
	for name, text := range surfaces {
		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Errorf("%s holds %q", name, leak)
			}
		}
	}
}

func mustGet(t *testing.T, e *env, id string) Outcome {
	t.Helper()
	o, err := e.svc.Get(context.Background(), core.RequestID(id))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func mustPreview(t *testing.T, e *env) Preview {
	t.Helper()
	p, err := e.svc.Preview(context.Background(), e.req("req_p", coreactions.DiagnosePipeline, "9001"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Invariant 10 (and the design's unreadable rule): whatever mix of reasons a
// repo has, if its folder cannot be read no kind starts an agent turn, creates
// a proposal or a task other than a waiting draft.
func TestInvariant_10_UnreadableRepoNeverRunsAnything(t *testing.T) {
	reasons := coreactions.Reasons()
	for seed := int64(0); seed < 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		e := newEnv(t)
		var rs []coreactions.Reason
		for _, x := range reasons {
			if r.Intn(2) == 0 || x == coreactions.ReasonUnreadable {
				rs = append(rs, x)
			}
		}
		e.ciFacts("FAIL")
		e.scope.set(func(f *coreactions.Facts) {
			f.Readable = false
			f.Reasons = rs
			f.BuildID, f.Caps.BuildLog = "bld_1", true
			f.UntrackedCount = 300
		})
		for i, k := range coreactions.Kinds() {
			src := map[coreactions.Kind]string{coreactions.DiagnosePipeline: "9001", coreactions.FixPipeline: "9001", coreactions.DiagnoseBuild: "bld_1",
				coreactions.ReviewMR: "3", coreactions.ImportIssue: "42"}[k]
			_, _ = e.svc.Dispatch(context.Background(), Input{Request: e.req(fmt.Sprintf("req_%d", i), k, src)})
		}
		// An unreadable folder only ever gets waiting drafts (no turn, no Code
		// task). A pending pull proposal may exist: it needs a confirmation and
		// re-reads the folder when confirmed.
		if e.tasks.agentCalls() != 0 {
			t.Fatalf("seed %d reasons %v: an unreadable repo started an agent: %s", seed, rs, e.effects())
		}
		for _, spec := range e.props.specs {
			if spec.Kind != core.ProposalPullFF {
				t.Fatalf("seed %d reasons %v: a %s proposal for an unreadable repo", seed, rs, spec.Kind)
			}
		}
		e.tasks.mu.Lock()
		ids := make([]core.TaskID, 0, len(e.tasks.byReq))
		for _, id := range e.tasks.byReq {
			ids = append(ids, id)
		}
		e.tasks.mu.Unlock()
		for _, id := range ids {
			task, err := e.store.GetTask(context.Background(), id)
			if err != nil || task.Status != core.TaskDraft || task.Mode != core.ModeAsk {
				t.Fatalf("seed %d reasons %v: task %s is %s/%s (%v), want an Ask draft", seed, rs, id, task.Status, task.Mode, err)
			}
		}
		for _, req := range e.tasks.requests {
			if req.Mode == string(core.ModeCode) {
				t.Fatalf("seed %d: a Code task for an unreadable repo", seed)
			}
		}
	}
}
