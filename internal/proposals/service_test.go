package proposals

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func (w *world) confirm(v View) View {
	w.t.Helper()
	out, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user")
	if err != nil {
		w.t.Fatalf("confirm: %v", err)
	}
	return out
}

func TestCreateStoresAPendingProposalWithItsAuditAndChangeSet(t *testing.T) {
	w := newWorld(t)
	a := w.create(w.commitAction("a.txt", "feat: a"))
	b := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: w.head(w.wtDir)})
	if a.Status != core.ProposalPending || len(a.Lines) == 0 || !a.FromAgent || a.Summary != "because" {
		t.Fatalf("view = %+v", a)
	}
	if a.ChangeSet == "" || a.ChangeSet != b.ChangeSet {
		t.Fatalf("change sets %q, %q: want one per task", a.ChangeSet, b.ChangeSet)
	}
	if n, err := w.svc.PendingCount(w.ctx, w.task.ID); err != nil || n != 2 {
		t.Fatalf("pending = %d, %v", n, err)
	}
	if got := w.auditActions(a.ID); !slices.Contains(got, "proposal.create") {
		t.Fatalf("audit = %v, want proposal.create", got)
	}
	if len(w.pub.events) < 2 {
		t.Fatalf("published %d events", len(w.pub.events))
	}
	list, err := w.svc.List(w.ctx, ListFilter{Task: w.task.ID})
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	if w.git.commitCount() != 0 {
		t.Fatal("creating a proposal ran a commit")
	}
}

func TestCreateRefusesWhatTheFactsForbid(t *testing.T) {
	w := newWorld(t)
	wt := coreprop.InWorktree{ID: wtID}
	head := w.head(w.wtDir)
	cases := map[string]struct {
		a      coreprop.Action
		reason coreprop.RefuseReason
	}{
		"protected path in a commit": {coreprop.Commit{Where: wt, Paths: []coreprop.RelPath{".claude/settings.json"}, Message: "m", ExpectHead: head}, coreprop.RefuseBadFacts},
		"protected folder":           {coreprop.DeleteFiles{Where: wt, Paths: []coreprop.RelPath{".husky/pre-commit"}}, coreprop.RefuseBadFacts},
		"worktree of another task":   {coreprop.Push{Where: coreprop.InWorktree{ID: "wt_other"}, Branch: branch, Remote: "origin", ExpectHead: head}, coreprop.RefuseBadFacts},
		"merge request on github":    {coreprop.MergeRequest{Where: wt, Branch: branch, Remote: "gh", TargetBranch: "main", Title: "t", ExpectHead: head}, coreprop.RefuseBadFacts},
	}
	w.git.AddRemote(w.wtDir, "gh", "https://github.example.test/acme/shop-api.git")
	for name, c := range cases {
		_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: c.a})
		var r *Refusal
		if !errors.Is(err, ErrRefused) || !errors.As(err, &r) || r.Reason != c.reason || r.UserMessage() == "" {
			t.Errorf("%s: err = %v, want a %s refusal with a message", name, err, c.reason)
		}
	}
	// A refusal stores nothing, not even the change set it would have created.
	if sets, _ := w.store.ListChangeSets(w.ctx, w.task.ID); len(sets) != 0 {
		t.Errorf("refused proposals left %d change sets", len(sets))
	}
	w.res.setUntracked(true)
	if _, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: w.commitAction("a.txt", "m")}); !errors.Is(err, ErrRefused) {
		t.Errorf("untracked repo: %v", err)
	}
}

func TestCreateRefusesTheFiftyFirstPendingProposal(t *testing.T) {
	w := newWorld(t)
	push := coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: w.head(w.wtDir)}
	for i := 0; i < coreprop.MaxPendingPerTask; i++ {
		w.create(push)
	}
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: push})
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != coreprop.RefuseTooMany {
		t.Fatalf("51st = %v, want too_many_pending", err)
	}
}

func TestConfirmRunsTheCommitAndRecordsTheResult(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("src/a.txt", "feat: add a"))
	before := w.head(w.wtDir)
	w.confirm(v)
	done := w.waitFor(v.ID, core.ProposalExecuted)
	if done.Result == nil || done.Result.CommitID == "" || done.Result.CommitID == before {
		t.Fatalf("result = %+v", done.Result)
	}
	info, _ := w.git.HeadInfo(w.ctx, w.wtDir)
	if info.ID != done.Result.CommitID || info.Parent != before || info.Message != "feat: add a" {
		t.Fatalf("HEAD = %+v", info)
	}
	got := w.auditActions(v.ID)
	for _, want := range []string{"proposal.create", "proposal.confirm", "proposal.execute"} {
		if !slices.Contains(got, want) {
			t.Errorf("audit %v lacks %s", got, want)
		}
	}
}

func TestConfirmWithAnotherHashChangesNothing(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	_, err := w.svc.Confirm(w.ctx, v.ID, strings.Repeat("0", 64), "user")
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != coreprop.RefuseHashMismatch {
		t.Fatalf("confirm with a wrong hash = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got, _ := w.svc.Get(w.ctx, v.ID); got.Status != core.ProposalPending || w.git.commitCount() != 0 {
		t.Fatalf("a wrong hash changed state to %s or committed", got.Status)
	}
	if slices.Contains(w.auditActions(v.ID), "proposal.confirm") {
		t.Fatal("a refused confirm was audited as a confirm")
	}
}

func TestDuplicateAndConcurrentConfirmsExecuteOnce(t *testing.T) {
	w := newWorld(t)
	w.git.delay = 20 * time.Millisecond
	v := w.create(w.commitAction("a.txt", "feat: a"))
	var ok, decided atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user")
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrAlreadyDecided):
				decided.Add(1)
			default:
				t.Errorf("confirm: %v", err)
			}
		}()
	}
	wg.Wait()
	w.waitFor(v.ID, core.ProposalExecuted)
	if ok.Load() != 1 || decided.Load() != 7 {
		t.Fatalf("%d confirms won and %d were told it was decided; want 1 and 7", ok.Load(), decided.Load())
	}
	if w.git.commitCount() != 1 {
		t.Fatalf("%d commits ran for one proposal", w.git.commitCount())
	}
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("confirm of an executed proposal = %v", err)
	}
	if _, err := w.svc.Reject(w.ctx, v.ID, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("reject of an executed proposal = %v", err)
	}
}

func TestConfirmationsOnOneRepoRunOneAtATimeInConfirmationOrder(t *testing.T) {
	w := newWorld(t)
	w.git.delay = 15 * time.Millisecond
	head := w.head(w.wtDir)
	var views []View
	for i := 0; i < 4; i++ {
		// The branch is bound to the worktree, so the pushes differ by remote.
		r := fmt.Sprintf("r%d", i)
		w.git.AddRemote(w.wtDir, r, fmt.Sprintf("https://gitlab.example.test/acme/shop-%d.git", i))
		views = append(views, w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: r, ExpectHead: head}))
	}
	for _, v := range views {
		w.confirm(v)
	}
	for _, v := range views {
		w.waitFor(v.ID, core.ProposalExecuted)
	}
	if w.git.maxFlight != 1 {
		t.Fatalf("%d git operations overlapped in one worktree", w.git.maxFlight)
	}
	for i, p := range w.git.pushes {
		if p.Remote != fmt.Sprintf("r%d", i) {
			t.Fatalf("push %d went to %s: not confirmation order", i, p.Remote)
		}
	}
}

func TestStaleCommitFailsAndIsNotRetryable(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	// Someone commits on the branch after the proposal was made.
	w.git.Write(w.wtDir, "other.txt", "x")
	_ = w.git.Fake.Add(w.ctx, w.wtDir, []string{"other.txt"})
	if _, err := w.git.Fake.Commit(w.ctx, w.wtDir, []string{"other.txt"}, "other", ""); err != nil {
		t.Fatal(err)
	}
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) || got.Retryable || got.Message == "" {
		t.Fatalf("failure = %+v", got)
	}
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("retry of a stale proposal = %v", err)
	}
}

func TestChangedFilesFailAsStaleToo(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	w.git.Write(w.wtDir, "a.txt", "the agent edited it again\n")
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) {
		t.Fatalf("failure = %+v", got)
	}
	if w.git.commitCount() != 0 {
		t.Fatal("a commit ran although the files differ from what the user reviewed")
	}
}

func TestRetryAfterANetworkFailureExecutes(t *testing.T) {
	w := newWorld(t)
	head := w.head(w.wtDir)
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: head})
	w.git.Fail["push"] = errors.New("could not resolve host")
	w.confirm(v)
	failed := w.waitFor(v.ID, core.ProposalFailed)
	if !failed.Retryable || failed.Attempt != 1 {
		t.Fatalf("failure = %+v", failed)
	}
	delete(w.git.Fail, "push")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(v.ID, core.ProposalExecuted)
	if got := w.git.RemoteTip(remoteURL, branch); got != head {
		t.Fatalf("remote tip = %q, want %q (%+v)", got, head, done)
	}
	if !slices.Contains(w.auditActions(v.ID), "proposal.retry") {
		t.Fatalf("audit = %v", w.auditActions(v.ID))
	}
}

func TestPushAlreadyOnTheRemoteIsExecutedWithoutAnotherPush(t *testing.T) {
	w := newWorld(t)
	head := w.head(w.wtDir)
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: head})
	// The first attempt landed but its result was lost.
	if _, err := w.git.Fake.Push(w.ctx, w.wtDir, gitPush(head)); err != nil {
		t.Fatal(err)
	}
	pushesBefore := len(w.git.pushes)
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalExecuted)
	if len(w.git.pushes) != pushesBefore {
		t.Fatal("pushed again although the remote already had the commit")
	}
}

func TestMergeRequestOpensWithPushOptionsAndKeepsTheLink(t *testing.T) {
	w := newWorld(t)
	head := w.head(w.wtDir)
	v := w.create(coreprop.MergeRequest{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", TargetBranch: "main",
		Title: "Add a", Description: "one line", ExpectHead: head})
	w.confirm(v)
	done := w.waitFor(v.ID, core.ProposalExecuted)
	if done.Result == nil || !strings.HasSuffix(done.Result.MRURL, "/merge_requests/1") {
		t.Fatalf("result = %+v", done.Result)
	}
	if len(w.git.pushes) != 1 || w.git.pushes[0].MR == nil || w.git.pushes[0].MR.Target != "main" {
		t.Fatalf("pushes = %+v", w.git.pushes)
	}
}

func TestPushToTheDefaultBranchFails(t *testing.T) {
	w := newWorld(t)
	w.git.SetBranch(w.wtDir, "main")
	w.res.mu.Lock()
	info := w.res.wts[wtID]
	info.Branch = "main"
	w.res.wts[wtID] = info
	w.res.mu.Unlock()
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: "main", Remote: "origin", ExpectHead: w.head(w.wtDir)})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || got.Retryable || w.git.RemoteTip(remoteURL, "main") != "" {
		t.Fatalf("failure = %+v", got)
	}
}

func TestCredentialsInTheRemoteSendNothing(t *testing.T) {
	w := newWorld(t, "tok3n-in-url")
	head := w.head(w.wtDir)
	w.git.SetRemoteURL(w.wtDir, "origin", "https://user:tok3n-in-url@gitlab.example.test/acme/shop-api.git")
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: head})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailUnsafeRemote) || len(w.git.pushes) != 0 {
		t.Fatalf("failure = %+v, pushes %d", got, len(w.git.pushes))
	}
	if strings.Contains(got.Message, "tok3n") {
		t.Fatalf("message holds the credential: %q", got.Message)
	}
}

func TestUntrackedRepoNeverRuns(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	// Untracked before the confirm: refused, still pending.
	w.res.setUntracked(true)
	_, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user")
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != coreprop.RefuseRepoUntracked {
		t.Fatalf("confirm on an untracked repo = %v", err)
	}
	// Untracked between the confirm and the executor: the executor stops.
	w.res.setUntracked(false)
	_, run, err := w.svc.persistDecision(w.ctx, v.ID, func(rec db.ProposalRecord, tracked bool, now time.Time) coreprop.Input {
		return coreprop.Confirm{Hash: v.ActionHash, By: "user", RepoTracked: tracked, Now: now}
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	w.res.setUntracked(true)
	run()
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailRepoUntracked) || got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	if ops := w.git.Ops(); slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "commit") || strings.HasPrefix(o, "add") }) {
		t.Fatalf("git ran in an untracked repo: %v", ops)
	}
}

func TestFailureMessagesAreRedactedInStateAndAudit(t *testing.T) {
	const secret = "s3cr3t-token-value-123"
	w := newWorld(t, secret)
	w.git.Fail["commit"] = fmt.Errorf("remote: denied for %s", secret)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if strings.Contains(got.Message, secret) {
		t.Fatalf("message holds the token: %q", got.Message)
	}
	rec, err := w.store.GetProposalRecord(w.ctx, v.ID)
	if err != nil || strings.Contains(rec.Proposal.State.(coreprop.Failed).Message, secret) {
		t.Fatalf("stored message holds the token: %v", err)
	}
	rows, _ := w.store.ListAudit(w.ctx, "proposal:"+string(v.ID), 50)
	for _, r := range rows {
		if strings.Contains(string(r.Detail), secret) || strings.Contains(r.Action, secret) {
			t.Fatalf("audit holds the token: %+v", r)
		}
	}
}

func TestAnExecutorPanicBecomesARetryableInterruption(t *testing.T) {
	w := newWorld(t)
	w.git.panicOn = "commit"
	v := w.create(w.commitAction("a.txt", "feat: a"))
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	// The queue still works after the panic.
	w.git.panicOn = ""
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(v.ID, core.ProposalExecuted)
}

func TestMainCheckoutInUseFailsRetryably(t *testing.T) {
	w := newWorld(t)
	w.lock.Deny(w.repo.ID, mainlockBuild("b1"))
	a := coreprop.AgentFile{Where: coreprop.MainCheckout{Repo: w.repo.ID}, Path: "CLAUDE.md", Content: "# rules\n"}
	v := w.create(a)
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || !got.Retryable || !strings.Contains(got.Message, "build") {
		t.Fatalf("failure = %+v", got)
	}
	w.lock.Allow(w.repo.ID)
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(v.ID, core.ProposalExecuted)
}

func TestRejectIsAudited(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	if _, err := w.svc.Reject(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.svc.Get(w.ctx, v.ID); got.Status != core.ProposalRejected {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("confirm after reject = %v", err)
	}
	if !slices.Contains(w.auditActions(v.ID), "proposal.reject") {
		t.Fatalf("audit = %v", w.auditActions(v.ID))
	}
}

func TestUnknownProposalIsNotFound(t *testing.T) {
	w := newWorld(t)
	for _, id := range []core.ProposalID{"prp_nope", "bad id"} {
		if _, err := w.svc.Get(w.ctx, id); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Get(%q) = %v", id, err)
		}
		if _, err := w.svc.Confirm(w.ctx, id, strings.Repeat("0", 64), "user"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Confirm(%q) = %v", id, err)
		}
	}
}
