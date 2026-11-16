package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// Safety invariants (docs/AGENTS.md section 6) for issue drafts: each test
// fails if the invariant is broken on the import or explicit start path.

func startDraftOf(t *testing.T, h *harness) core.TaskID {
	t.Helper()
	d, err := h.svc.ImportIssueDraft(context.Background(), issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	return d.Task.ID
}

func refusedInert(t *testing.T, h *harness, id core.TaskID, err error, reason string) {
	t.Helper()
	if !rejectedWith(err, reason) {
		t.Fatalf("want refusal %s, got %v", reason, err)
	}
	st, gerr := h.store.GetTask(context.Background(), id)
	turns, _ := h.store.ListTaskTurns(context.Background(), id)
	if gerr != nil || st.Status != core.TaskDraft || len(turns) != 0 || len(h.adapter.Starts()) != 0 {
		t.Fatalf("a refused start did not leave the draft inert: %s %d turns %d starts", st.Status, len(turns), len(h.adapter.Starts()))
	}
}

// Invariants 2 and 5: a draft edits the main checkout only on explicit opt-in,
// with a warning, an enforcing adapter and the shared editor lock; Code stays
// in a worktree whatever the origin.
func TestInvariant_2_5_DraftDirectEditNeedsOptInEnforcementAndWarning(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id := startDraftOf(t, h)
	_, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch}, "")
	refusedInert(t, h, id, err, "needs_opt_in")
	_, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch, OptIn: true}, "")
	refusedInert(t, h, id, err, "limited_enforcement")
	// Project mode is not a choice for a repo-scope draft.
	if _, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeProject, OptIn: true}, ""); err == nil {
		t.Fatal("a repo-scope draft started in project mode")
	}
	h.adapter.caps.Enforcement = agents.EnforcementHook
	res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch, OptIn: true}, "")
	if err != nil || res.Warning != "edits_main_checkout" {
		t.Fatalf("direct edit: %+v %v", res, err)
	}
	h.waitStatus(id, core.TaskReview)
	starts := h.adapter.Starts()
	if len(starts) != 1 || !samePath(starts[0].Dir, h.repos[0].Path) {
		t.Fatalf("starts %+v", starts)
	}
	for _, root := range starts[0].Policy.WriteRoots {
		if !samePath(root, h.repos[0].Path) {
			t.Fatalf("write root %s escapes the task's repo", root)
		}
	}
	// Code never touches the main checkout.
	h2 := newHarness(t, []string{"shop-api"})
	h2.start()
	id2 := startDraftOf(t, h2)
	if _, err := h2.svc.StartDraft(ctx, coretasks.DraftStart{Task: id2, Mode: core.ModeCode}, ""); err != nil {
		t.Fatal(err)
	}
	h2.waitStatus(id2, core.TaskReview)
	s2 := h2.adapter.Starts()
	if len(s2) != 1 || samePath(s2[0].Dir, h2.repos[0].Path) || len(s2[0].Policy.WriteRoots) == 0 {
		t.Fatalf("Code ran outside a worktree: %+v", s2)
	}
	for _, root := range s2[0].Policy.WriteRoots {
		if samePath(root, h2.repos[0].Path) {
			t.Fatalf("Code may write the main checkout %s", root)
		}
	}
}

// Invariant 6: only tracked repos of the project can be imported into, and a
// refusal leaves no task, link or audit entry.
func TestInvariant_6_ImportRefusesRepoOutsideTheProjectWithoutCollateralWrites(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.start()
	other := dbtest.Project(t, h.store, core.Project{Name: "acme-admin", Path: filepath.Join(t.TempDir(), "acme-admin")})
	foreign := dbtest.Repo(t, h.store, core.Repo{ProjectID: other.ID, Name: "shop-admin", Path: filepath.Join(other.Path, "shop-admin"), DefaultBranch: "main"})
	dbtest.UntrackRepo(t, h.store, h.repos[1])
	before, _ := h.store.ListAudit(ctx, "", 1000)
	for name, repo := range map[string]core.RepoID{"foreign": foreign.ID, "untracked": h.repos[1].ID, "unknown": "rep_nothere"} {
		spec := issueSpec(h)
		spec.RequestID, spec.Repo = core.RequestID("req_"+name), repo
		if _, err := h.svc.ImportIssueDraft(ctx, spec); !rejectedWith(err, "repo_untracked") {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := h.svc.LookupRequest(ctx, spec.RequestID); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("%s: a link was written: %v", name, err)
		}
	}
	after, _ := h.store.ListAudit(ctx, "", 1000)
	mustTaskCount(t, h, 0, "refused imports")
	if len(after) != len(before) {
		t.Fatalf("refused imports wrote %d audit entries", len(after)-len(before))
	}
}

// Invariant 8 and the failure matrix: the task, the request link, the turn and
// the audit entries commit together. A fault at the last write (a duplicate
// audit ID) or at the task or turn write (a duplicate ID) rolls everything
// back, propagates the error, and never reaches the adapter.
func TestInvariant_8_ImportAndSubmissionFaultsRollBackAllWrites(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	h.start()
	first, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil || !h.hasAudit(first.Task.ID, "task.import") {
		t.Fatalf("an import must be audited: %v %v", err, h.audits(first.Task.ID))
	}
	sub, err := h.svc.SubmitWithRequest(ctx, "req_ok", h.msg("ask", "explain", 0))
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(sub.Task.ID, core.TaskDone)
	entries, _ := h.store.ListAudit(ctx, "", 1000)
	turns, _ := h.store.ListTaskTurns(ctx, sub.Task.ID)
	startsBefore := len(h.adapter.Starts())
	realID := h.svc.c.NewID
	collide := func(prefix, existing string) {
		h.svc.c.NewID = func(p string) string {
			if p == prefix {
				return existing
			}
			return realID(p)
		}
	}
	snapshot := func(name string) {
		t.Helper()
		if list, _ := h.store.ListTasks(ctx, h.project.ID); len(list) != 2 {
			t.Fatalf("%s: %d tasks after the fault", name, len(list))
		}
		if now, _ := h.store.ListAudit(ctx, "", 1000); len(now) != len(entries) {
			t.Fatalf("%s: audit entries %d -> %d, a preceding write survived", name, len(entries), len(now))
		}
		if _, err := h.svc.LookupRequest(ctx, "req_fault"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("%s: the request link survived: %v", name, err)
		}
		if got := len(h.adapter.Starts()); got != startsBefore {
			t.Fatalf("%s: the adapter ran for a rolled-back write", name)
		}
	}
	// Import: the audit entry is the last write, after the task and the link.
	collide("aud", string(entries[0].ID))
	spec := issueSpec(h)
	spec.RequestID, spec.Issue.Number = "req_fault", 999
	if _, err := h.svc.ImportIssueDraft(ctx, spec); err == nil {
		t.Fatal("import: the audit fault was swallowed")
	}
	snapshot("import audit fault")
	// Import: the task ID is taken.
	h.svc.c.NewID = realID
	collide("tsk", string(first.Task.ID))
	if _, err := h.svc.ImportIssueDraft(ctx, spec); err == nil {
		t.Fatal("import: the task fault was swallowed")
	}
	snapshot("import task fault")
	// Submission: the task ID, the turn ID and the audit ID are taken.
	for _, c := range []struct{ name, prefix, id string }{
		{"submission task fault", "tsk", string(first.Task.ID)},
		{"submission turn fault", "trn", string(turns[0].ID)},
		{"submission audit fault", "aud", string(entries[0].ID)},
	} {
		h.svc.c.NewID = realID
		collide(c.prefix, c.id)
		if _, err := h.svc.SubmitWithRequest(ctx, "req_fault", h.msg("ask", "hello", 0)); err == nil {
			t.Fatalf("%s: the fault was swallowed", c.name)
		}
		snapshot(c.name)
	}
	// With the fault gone the same request id succeeds exactly once.
	h.svc.c.NewID = realID
	ok, err := h.svc.SubmitWithRequest(ctx, "req_fault", h.msg("ask", "hello", 0))
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(ok.Task.ID, core.TaskDone)
	if again, err := h.svc.SubmitWithRequest(ctx, "req_fault", h.msg("ask", "hello", 0)); err != nil || again.Task.ID != ok.Task.ID {
		t.Fatalf("replay after recovery: %+v %v", again, err)
	}
}

// Invariant 9: an Ask draft start keeps the integrity check; a tree change
// needs the user, and nothing is reverted.
func TestInvariant_9_AskDraftStartIsIntegrityChecked(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.during = func(o agents.StartOptions) {
		_ = os.WriteFile(filepath.Join(o.Dir, "README.md"), []byte("changed by the agent\n"), 0o600)
	}
	h.start()
	id := startDraftOf(t, h)
	if _, err := h.svc.StartDraft(context.Background(), coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, ""); err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) || !h.hasAudit(id, "guard.integrity") {
		t.Fatalf("outcome %#v audit %v", outcomeOf(t, st), h.audits(id))
	}
	if h.snap.n.Load() < 2 {
		t.Fatalf("%d snapshots, want before and after", h.snap.n.Load())
	}
}

// Invariant 10: an untrusted repo only gets Ask; the refusal leaves the draft
// inert and a following Ask start works.
func TestInvariant_10_UntrustedRepoDraftOnlyStartsInAsk(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.configs = []string{".claude"}
	h.scanner.set(h.repos[0].Path, claudeConfig)
	h.start()
	id := startDraftOf(t, h)
	for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch} {
		_, err := h.svc.StartDraft(context.Background(), coretasks.DraftStart{Task: id, Mode: mode, OptIn: true}, "")
		refusedInert(t, h, id, err, "repo_untrusted")
	}
	if _, err := h.svc.StartDraft(context.Background(), coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, ""); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(id, core.TaskDone)
}

// A crash after the commit of the explicit start and before the run is
// queued: after the restart the draft has exactly one first turn, at most one
// agent run, and a second start is refused.
func TestDraftStartCrashAfterCommitReconcilesOnce(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, c := range cmds {
			if _, ok := c.(coretasks.Enqueue); ok {
				return true
			}
		}
		return false
	}
	h.start()
	ctx := context.Background()
	id := startDraftOf(t, h)
	res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "")
	if err != nil || len(h.adapter.Starts()) != 0 {
		t.Fatalf("%+v %v starts %d", res, err, len(h.adapter.Starts()))
	}
	h.restart()
	h.waitStatus(id, core.TaskDone)
	turns, _ := h.store.ListTaskTurns(ctx, id)
	if len(turns) != 1 || turns[0].ID != res.Turn || len(h.adapter.Starts()) != 1 {
		t.Fatalf("turns %v starts %d", turns, len(h.adapter.Starts()))
	}
	if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, ""); err == nil {
		t.Fatal("second start admitted")
	}
	if turns, _ = h.store.ListTaskTurns(ctx, id); len(turns) != 1 || len(h.adapter.Starts()) != 1 {
		t.Fatal("a second turn appeared")
	}
}

// Drafts that edit the main checkout share the editor lock: a build or other
// editor makes the start wait (never run beside it), and cancelling a waiting
// or running draft releases the lock for the next one.
func TestDraftDirectEditSharesEditorLockAndCancelReleasesIt(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.gate = make(chan struct{})
	h.start()
	spec := issueSpec(h)
	a, _ := h.svc.ImportIssueDraft(ctx, spec)
	spec.RequestID, spec.Issue.Number = "req_b", 13
	b, _ := h.svc.ImportIssueDraft(ctx, spec)
	rel, _, ok := h.lock.TryAcquire([]core.RepoID{h.repos[0].ID}, "build:1")
	if !ok {
		t.Fatal("no lock")
	}
	start := func(id core.TaskID) {
		if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch, OptIn: true}, ""); err != nil {
			t.Fatal(err)
		}
	}
	start(a.Task.ID)
	time.Sleep(150 * time.Millisecond)
	if len(h.adapter.Starts()) != 0 || h.state(a.Task.ID).Row.Status != core.TaskQueued {
		t.Fatal("a draft edited the main checkout beside a build")
	}
	rel()
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the first editor")
	start(b.Task.ID)
	time.Sleep(150 * time.Millisecond)
	if len(h.adapter.Starts()) != 1 {
		t.Fatal("two drafts edit one main checkout")
	}
	if _, err := h.svc.Cancel(ctx, a.Task.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 2 }, "the second editor after the cancel released the lock")
	close(h.adapter.gate)
	h.waitStatus(b.Task.ID, core.TaskReview)
	waitFor(t, 5*time.Second, func() bool { return h.lock.held(h.repos[0].ID) == "" }, "the lock to be released")
	if m := h.adapter.maxLive.Load(); m != 1 {
		t.Fatalf("up to %d editors at once", m)
	}
}

// Two independent services on one database arbitrate an explicit start
// through the stored version: exactly one first turn.
func TestExplicitStartAcrossTwoServicesAdmitsOneFirstTurn(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	svc1 := h.start()
	id := startDraftOf(t, h)
	cfg := h.cfg
	svc2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc2.Close() })
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Service{svc1, svc2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d starts won: %v", ok, errs)
	}
	h.waitStatus(id, core.TaskDone)
	if turns, _ := h.store.ListTaskTurns(ctx, id); len(turns) != 1 || len(h.adapter.Starts()) != 1 {
		t.Fatalf("turns %d starts %d", len(turns), len(h.adapter.Starts()))
	}
}

// noProbeLock hides Held, so only TryAcquire (the authority) guards the editor
// lock and the LocksDenied path is the one exercised.
type noProbeLock struct{ l *fakeMainLock }

func (n noProbeLock) TryAcquire(repos []core.RepoID, owner string) (Release, string, bool) {
	return n.l.TryAcquire(repos, owner)
}

func TestDraftDirectEditHonoursTheEditorLockWithoutAProbe(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.tune = func(c *Config) { c.MainLock = noProbeLock{h.lock} }
	h.start()
	id := startDraftOf(t, h)
	rel, _, ok := h.lock.TryAcquire([]core.RepoID{h.repos[0].ID}, "build:1")
	if !ok {
		t.Fatal("no lock")
	}
	if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch, OptIn: true}, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(h.adapter.Starts()) != 0 {
		t.Fatal("the draft edited the main checkout while another holder had it")
	}
	rel()
	h.waitStatus(id, core.TaskReview)
	if len(h.adapter.Starts()) != 1 || h.lock.held(h.repos[0].ID) != "" {
		t.Fatalf("starts %d, holder %q", len(h.adapter.Starts()), h.lock.held(h.repos[0].ID))
	}
}
