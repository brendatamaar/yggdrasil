package actions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Only tracked READY repos are ever offered or launched (invariants 5 and 6).
func TestDispatchOnlyTrackedReady(t *testing.T) {
	t.Run("untracked, reserved or unknown repo is not found", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		e.scope.untrack()
		if _, err := e.svc.Offerings(context.Background(), Scope{Repo: e.repo.ID}); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("offerings of an untracked repo: %v, want not found", err)
		}
		out := e.dispatch("req_1", coreactions.DiagnoseRepo, "")
		if out.State != db.ActionRefused || out.Refusal != coreactions.RefuseUntracked {
			t.Fatalf("outcome %+v, want refused untracked", out)
		}
		if e.tasks.agentCalls() != 0 || e.tasks.taskCount() != 0 || e.props.creates != 0 {
			t.Fatal("something was launched for an untracked repo")
		}
		if _, err := e.store.GetActionRequest(context.Background(), "req_1"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("a request row exists for a refusal before intent: %v", err)
		}
		if !strings.Contains(e.auditText(), "action.refused") {
			t.Error("the refusal is not audited")
		}
	})
	t.Run("a repo outside the request is another scope", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		r, err := coreactions.ParseRequest("req_1", "rep_foreign", "diagnose_repo", "", "mock", "")
		if err != nil {
			t.Fatal(err)
		}
		out, err := e.svc.Dispatch(context.Background(), Input{Request: r})
		if err != nil || out.State != db.ActionRefused || out.Refusal != coreactions.RefuseUntracked || e.tasks.agentCalls() != 0 {
			t.Fatalf("foreign repo: %+v, %v", out, err)
		}
	})
	t.Run("untracked after the preview is rechecked at execution", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		// Facts is read at prepare (1) and again at execution (2): untrack
		// before the second read.
		e.scope.onFacts = func(n int, s *fakeScope) {
			if n == 2 {
				s.untrack()
			}
		}
		out := e.dispatch("req_1", coreactions.DiagnoseRepo, "")
		if out.State != db.ActionRefused || out.Refusal != coreactions.RefuseUntracked {
			t.Fatalf("outcome %+v, want refused untracked", out)
		}
		if e.tasks.agentCalls() != 0 || e.tasks.drafts != 0 {
			t.Fatal("a task was launched after the repo was untracked")
		}
		if rec := e.record("req_1"); rec.State != db.ActionRefused || rec.Task != "" {
			t.Fatalf("stored %+v", rec)
		}
	})
	t.Run("a moved HEAD refuses and starts nothing", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		e.scope.onFacts = func(n int, s *fakeScope) {
			if n == 2 {
				s.set(func(f *coreactions.Facts) { f.Head = head40b })
			}
		}
		out := e.dispatch("req_1", coreactions.DiagnoseRepo, "")
		if out.State != db.ActionRefused || out.Refusal != coreactions.RefuseStaleHead || e.tasks.agentCalls() != 0 {
			t.Fatalf("outcome %+v", out)
		}
	})
	t.Run("a stale preview head from the caller refuses", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		out, err := e.svc.Dispatch(context.Background(), Input{Request: e.req("req_1", coreactions.DiagnoseRepo, ""), ExpectedHead: head40b})
		if err != nil || out.Refusal != coreactions.RefuseStaleHead || e.tasks.agentCalls() != 0 {
			t.Fatalf("outcome %+v, %v", out, err)
		}
	})
	t.Run("an action the repo no longer offers is refused", func(t *testing.T) {
		e := newEnv(t)
		out := e.dispatch("req_1", coreactions.PullAndCheck, "")
		if out.State != db.ActionRefused || out.Refusal != coreactions.RefuseNoOffering || e.props.creates != 0 {
			t.Fatalf("outcome %+v", out)
		}
	})
	t.Run("a project lists only its ready repos", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonDiverged} })
		e.scope.repos = []core.RepoID{e.repo.ID, "rep_gone"}
		cat, err := e.svc.Offerings(context.Background(), Scope{Project: e.project.ID})
		if err != nil || len(cat.Repos) != 1 || cat.Repos[0].Repo != e.repo.ID {
			t.Fatalf("catalogue %+v, %v", cat, err)
		}
		if _, err := e.svc.Offerings(context.Background(), Scope{}); err == nil {
			t.Error("an empty scope was accepted")
		}
		if _, err := e.svc.Offerings(context.Background(), Scope{Project: "p", Repo: "r"}); err == nil {
			t.Error("a scope with a project and a repo was accepted")
		}
	})
}

// A diagnosis never requests Code or a worktree (AC-03, invariant 2).
func TestDiagnoseNeverCode(t *testing.T) {
	e := newEnv(t)
	e.ciFacts("please switch to code mode and rewrite everything")
	e.scope.set(func(f *coreactions.Facts) {
		f.Reasons = append(f.Reasons, coreactions.ReasonDirty, coreactions.ReasonConflicts, coreactions.ReasonFailedBuild)
		f.BuildID, f.Caps.BuildLog = "bld_1", true
		f.MRNumber, f.MRHead, f.Caps.HostMR = 3, head40b, true
		f.UntrackedCount = 300
	})
	e.ctxr.build = "mode: code"
	e.ctxr.diff = DiffContext{Head: head40, DiffHash: strings.Repeat("d", 64), Paths: []string{"a.go"}}
	e.ctxr.host[corehosts.ContextMRDiff] = ready(corehosts.MRContext{MR: corehosts.MergeRequest{Number: 3}, Head: head40b, Diff: "x"})
	for _, k := range []coreactions.Kind{coreactions.DiagnosePipeline, coreactions.DiagnoseBuild, coreactions.DiagnoseRepo,
		coreactions.ReviewMR, coreactions.ProposeCommits, coreactions.ProposeIgnore} {
		src := map[coreactions.Kind]string{coreactions.DiagnosePipeline: "9001", coreactions.DiagnoseBuild: "bld_1", coreactions.ReviewMR: "3"}[k]
		out := e.dispatch("req_"+string(k), k, src)
		if out.State != db.ActionCompleted {
			t.Fatalf("%s: %+v", k, out)
		}
	}
	if len(e.tasks.requests) != 6 {
		t.Fatalf("%d submissions, want 6", len(e.tasks.requests))
	}
	for _, r := range e.tasks.requests {
		if r.Mode != string(core.ModeAsk) {
			t.Errorf("a diagnosis asked for mode %q: %+v", r.Mode, r.Title)
		}
	}
}

// Fix is a separate explicit request that creates a new Code task; the
// diagnosis task is never re-moded (AC-03).
func TestFixExplicit(t *testing.T) {
	e := newEnv(t)
	e.ciFacts("FAIL")
	diag := e.dispatch("req_diag", coreactions.DiagnosePipeline, "9001")
	if diag.State != db.ActionCompleted || e.tasks.requests[0].Mode != "ask" {
		t.Fatalf("diagnosis %+v / %+v", diag, e.tasks.requests)
	}
	if len(e.tasks.requests) != 1 {
		t.Fatal("a fix started without an explicit request")
	}
	fix := e.dispatch("req_fix", coreactions.FixPipeline, "9001")
	if fix.State != db.ActionCompleted || fix.Result.TaskID == diag.Result.TaskID {
		t.Fatalf("fix %+v must be a new task, not the diagnosis %+v", fix, diag)
	}
	if got := e.tasks.requests[1]; got.Mode != "code" || got.Scope != "repo" || got.Repos[0] != string(e.repo.ID) {
		t.Fatalf("fix submitted as %+v", got)
	}
	if e.tasks.taskCount() != 2 {
		t.Fatalf("%d tasks, want 2", e.tasks.taskCount())
	}
	// A fix for a source that is not the failed run is not offered.
	if out := e.dispatch("req_fix2", coreactions.FixPipeline, "123"); out.State != db.ActionRefused || out.Refusal != coreactions.RefuseScopeChanged {
		t.Fatalf("fix of a foreign run: %+v", out)
	}
	// The Code task goes through the ordinary admission: a refusal there is
	// reported and nothing is faked.
	e.tasks.failBefore = &tasks.RejectedError{Reason: "repo_untrusted", Message: "Trust this repo first."}
	out := e.dispatch("req_fix3", coreactions.FixPipeline, "9001")
	if out.State != db.ActionRefused || out.Refusal != coreactions.RefuseAdmission || out.Message != "Trust this repo first." {
		t.Fatalf("admission refusal: %+v", out)
	}
}

// An issue import is a draft: zero agent calls, no turn, process or worktree
// (AC-05).
func TestIssueImportStaysDraft(t *testing.T) {
	e := newEnv(t)
	e.scope.set(func(f *coreactions.Facts) { f.Caps.HostIssues = true })
	e.ctxr.host[corehosts.ContextIssue] = ready(corehosts.IssueDetail{
		Summary: corehosts.IssueSummary{Number: 42, Title: "Cart total is wrong", Labels: []string{"bug"}}, Body: "Steps: add two items."})
	out := e.dispatch("req_1", coreactions.ImportIssue, "42")
	if out.State != db.ActionCompleted || out.Result.TaskID == "" {
		t.Fatalf("outcome %+v", out)
	}
	if e.tasks.agentCalls() != 0 {
		t.Fatalf("%d agent calls for an import, want 0", e.tasks.agentCalls())
	}
	if e.tasks.drafts != 1 {
		t.Fatalf("%d draft imports, want 1", e.tasks.drafts)
	}
	task, err := e.store.GetTask(context.Background(), out.Result.TaskID)
	if err != nil || task.Status != core.TaskDraft || task.Source == nil {
		t.Fatalf("task %+v, %v: want a draft with its source", task, err)
	}
	if !strings.Contains(task.Prompt, "Steps: add two items.") || !strings.Contains(task.Prompt, "Cart total is wrong") {
		t.Errorf("the draft brief lacks the issue: %q", task.Prompt)
	}
	for _, bad := range []struct {
		name  string
		setup func(e *env)
	}{
		{"no host capability", func(e *env) { e.scope.set(func(f *coreactions.Facts) { f.Caps.HostIssues = false }) }},
		{"the host blocks the issue", func(e *env) {
			e.ctxr.host[corehosts.ContextIssue] = hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}
		}},
		{"no linked host", func(e *env) { e.scope.target.HasHost = false }},
	} {
		e2 := newEnv(t)
		e2.scope.set(func(f *coreactions.Facts) { f.Caps.HostIssues = true })
		e2.ctxr.host[corehosts.ContextIssue] = ready(corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 1, Title: "t"}})
		bad.setup(e2)
		o := e2.dispatch("req_x", coreactions.ImportIssue, "1")
		if o.State != db.ActionRefused || e2.tasks.drafts != 0 || e2.tasks.agentCalls() != 0 {
			t.Errorf("%s: %+v, drafts %d", bad.name, o, e2.tasks.drafts)
		}
	}
}

// A retried or double-clicked dispatch has one effect (AC-05, idempotency).
func TestDuplicateDispatchOneEffect(t *testing.T) {
	t.Run("sequential repeat returns the first result", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		first := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		again := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		if again.Result.TaskID != first.Result.TaskID || !again.Replayed || first.Replayed {
			t.Fatalf("first %+v, again %+v", first, again)
		}
		if e.tasks.submits != 1 || e.tasks.taskCount() != 1 {
			t.Fatalf("%d submissions, %d tasks, want 1 each", e.tasks.submits, e.tasks.taskCount())
		}
	})
	t.Run("concurrent clicks", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		var wg sync.WaitGroup
		outs := make([]Outcome, 8)
		for i := range outs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				o, err := e.svc.Dispatch(context.Background(), Input{Request: e.req("req_1", coreactions.DiagnosePipeline, "9001")})
				if err != nil {
					t.Error(err)
				}
				outs[i] = o
			}()
		}
		wg.Wait()
		for _, o := range outs {
			if o.State != db.ActionCompleted || o.Result.TaskID != outs[0].Result.TaskID {
				t.Fatalf("outcomes differ: %+v vs %+v", o, outs[0])
			}
		}
		if e.tasks.submits != 1 {
			t.Fatalf("%d submissions, want 1", e.tasks.submits)
		}
	})
	t.Run("a changed request under the same id is refused", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		changed := e.req("req_1", coreactions.DiagnosePipeline, "9001")
		changed.Agent = "other"
		if _, err := e.svc.Dispatch(context.Background(), Input{Request: changed}); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("err = %v, want a digest mismatch", err)
		}
		fix := e.req("req_1", coreactions.FixPipeline, "9001")
		if _, err := e.svc.Dispatch(context.Background(), Input{Request: fix}); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("err = %v, want a digest mismatch for another kind", err)
		}
		if e.tasks.submits != 1 {
			t.Fatalf("%d submissions, want 1", e.tasks.submits)
		}
		if _, err := e.svc.Dispatch(context.Background(), Input{Request: e.req("req_2", coreactions.DiagnosePipeline, "9001"), Digest: "wrong"}); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("a wrong preview digest: %v", err)
		}
	})
	t.Run("a repeated pull makes one proposal", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
		a := e.dispatch("req_1", coreactions.PullAndCheck, "")
		b := e.dispatch("req_1", coreactions.PullAndCheck, "")
		if e.props.creates != 1 || !reflect.DeepEqual(a.Result.ProposalIDs, b.Result.ProposalIDs) {
			t.Fatalf("creates %d, %+v vs %+v", e.props.creates, a, b)
		}
	})
}

// Exactly-once task and proposal creation across a crash at every boundary,
// through restart and Recover.
func TestCrashAtEveryBoundary(t *testing.T) {
	restart := func(e *env) *Service {
		_ = e.svc.Close()
		e.svc = e.build(e.store)
		e.t.Cleanup(func() { _ = e.svc.Close() })
		return e.svc
	}
	ctx := context.Background()

	t.Run("after the intent, before the effect", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		e.tasks.beforeCreate = e.svc.cancel // the process dies here, before anything exists
		e.tasks.failBefore = errors.New("process died")
		_, _ = e.svc.Dispatch(ctx, Input{Request: e.req("req_1", coreactions.DiagnosePipeline, "9001")})
		e.tasks.failBefore, e.tasks.beforeCreate = nil, nil
		s := restart(e)
		if err := s.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		rec := e.record("req_1")
		if rec.State != db.ActionRefused || rec.Refusal != coreactions.RefuseAbsentAfterLoss || e.tasks.taskCount() != 0 {
			t.Fatalf("stored %+v, tasks %d: nothing exists, so the request is refused for a retry", rec, e.tasks.taskCount())
		}
	})
	t.Run("effect done, reply lost, same process", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		e.tasks.failAfter = errors.New("timeout waiting for the reply")
		out := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		if out.State != db.ActionCompleted || out.Result.TaskID == "" || e.tasks.submits != 1 || e.tasks.taskCount() != 1 {
			t.Fatalf("outcome %+v, submits %d", out, e.tasks.submits)
		}
	})
	t.Run("effect done, result commit lost", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		dying := &dyingStore{RequestStore: e.store}
		_ = e.svc.Close()
		e.svc = e.build(dying)
		e.tasks.afterCreate = dying.kill // the process dies right after the task exists
		out, _ := e.svc.Dispatch(ctx, Input{Request: e.req("req_1", coreactions.DiagnosePipeline, "9001")})
		if out.State != db.ActionSubmitting {
			t.Fatalf("outcome %+v, want submitting", out)
		}
		if rec := e.record("req_1"); rec.State != db.ActionSubmitting {
			t.Fatalf("stored %+v", rec)
		}
		e.tasks.afterCreate = nil
		s := restart(e)
		if err := s.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		rec := e.record("req_1")
		if rec.State != db.ActionCompleted || rec.Task == "" || e.tasks.submits != 1 || e.tasks.taskCount() != 1 {
			t.Fatalf("stored %+v, submits %d, tasks %d: want one completed task", rec, e.tasks.submits, e.tasks.taskCount())
		}
		// A user retry after recovery returns the same task and starts nothing.
		again := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		if again.Result.TaskID != rec.Task || e.tasks.submits != 1 {
			t.Fatalf("retry %+v, submits %d", again, e.tasks.submits)
		}
		if n := strings.Count(e.auditText(), "action.completed"); n != 1 {
			t.Errorf("%d completion audit entries, want 1", n)
		}
	})
	t.Run("proposal created, result commit lost", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
		dying := &dyingStore{RequestStore: e.store}
		_ = e.svc.Close()
		e.svc = e.build(dying)
		e.props.afterWrite = dying.kill
		_, _ = e.svc.Dispatch(ctx, Input{Request: e.req("req_1", coreactions.PullAndCheck, "")})
		e.props.afterWrite = nil
		s := restart(e)
		if err := s.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		rec := e.record("req_1")
		if rec.State != db.ActionCompleted || len(rec.Proposals) != 1 || e.props.creates != 1 {
			t.Fatalf("stored %+v, creates %d", rec, e.props.creates)
		}
	})
	t.Run("proposal lost reply, found by the read-only lookup", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
		e.props.failAfter = errors.New("connection reset")
		out := e.dispatch("req_1", coreactions.PullAndCheck, "")
		if out.State != db.ActionCompleted || len(out.Result.ProposalIDs) != 1 || e.props.creates != 1 {
			t.Fatalf("outcome %+v, creates %d", out, e.props.creates)
		}
	})
	t.Run("nothing was created before the crash: proposals", func(t *testing.T) {
		e := newEnv(t)
		e.scope.set(func(f *coreactions.Facts) { f.Reasons = []coreactions.Reason{coreactions.ReasonBehind} })
		if _, err := e.store.GetActionRequest(ctx, "req_1"); !errors.Is(err, db.ErrNotFound) {
			t.Fatal("unexpected row")
		}
		// A row left submitting with no proposal behind it.
		e.insertSubmitting("req_1", coreactions.PullAndCheck, "")
		if err := e.svc.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		rec := e.record("req_1")
		if rec.State != db.ActionRefused || e.props.creates != 0 {
			t.Fatalf("stored %+v, creates %d", rec, e.props.creates)
		}
	})
	t.Run("an unanswerable lookup keeps the request and retries without a second submit", func(t *testing.T) {
		e := newEnv(t)
		e.ciFacts("FAIL")
		e.tasks.failAfter = errors.New("timeout")
		e.tasks.lookupErr = errors.New("database busy")
		out := e.dispatch("req_1", coreactions.DiagnosePipeline, "9001")
		if out.State != db.ActionSubmitting {
			t.Fatalf("outcome %+v, want submitting (the answer is not known)", out)
		}
		e.tasks.mu.Lock()
		e.tasks.lookupErr = nil
		e.tasks.mu.Unlock()
		waitFor(t, func() bool { return e.record("req_1").State == db.ActionCompleted })
		if e.tasks.submits != 1 || e.tasks.taskCount() != 1 {
			t.Fatalf("submits %d tasks %d", e.tasks.submits, e.tasks.taskCount())
		}
	})
	t.Run("Close is idempotent and stops the workers", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Dispatch(ctx, Input{Request: e.req("req_1", coreactions.DiagnoseRepo, "")}); !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A request row left submitting, as a crash after the intent leaves it.
func (e *env) insertSubmitting(id string, kind coreactions.Kind, source string) {
	e.t.Helper()
	r := e.req(id, kind, source)
	rec := db.ActionRecord{Request: r, Digest: r.Digest(), State: db.ActionSubmitting, Attempt: 1, Key: "action:" + id + ":1", CreatedAt: nowUTC()}
	if err := e.store.Commit(context.Background(), func(tx *db.Tx) error {
		_, _, err := tx.BeginActionRequest(context.Background(), rec)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

// Nothing here confirms or executes a proposal (invariant 2): the creator port
// can only create and find, and a created proposal stays pending.
func TestNoProposalExecutesWithoutConfirmation(t *testing.T) {
	typ := reflect.TypeOf((*ProposalCreator)(nil)).Elem()
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	if fmt.Sprint(names) != "[CreateForRequest FindForRequest]" {
		t.Fatalf("ProposalCreator methods %v: the shell must not be able to confirm or run a proposal", names)
	}
	e := newEnv(t)
	e.scope.set(func(f *coreactions.Facts) {
		f.Reasons = []coreactions.Reason{coreactions.ReasonBehind, coreactions.ReasonDirty}
	})
	e.ctxr.diff = DiffContext{Head: head40, DiffHash: strings.Repeat("d", 64), Paths: []string{"a.go"}}
	out := e.dispatch("req_1", coreactions.PullAndCheck, "")
	e.dispatch("req_2", coreactions.ProposeCommits, "")
	if out.State != db.ActionCompleted {
		t.Fatalf("%+v", out)
	}
	for _, id := range out.Result.ProposalIDs {
		p, err := e.store.GetProposal(context.Background(), id)
		if err != nil || p.Status != core.ProposalPending || p.DecidedAt != nil {
			t.Fatalf("proposal %s is %+v, %v: it must stay pending until the user confirms", id, p.Status, err)
		}
	}
	// The commit grouping task only drafts; the shell created no commit proposal.
	if e.props.creates != 1 {
		t.Fatalf("%d proposal creations, want only the pull", e.props.creates)
	}
}
