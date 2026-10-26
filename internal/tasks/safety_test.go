package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
)

func transcript(t *testing.T, h *harness, id core.TaskID) string {
	t.Helper()
	ev, err := h.store.ListTaskEvents(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range ev {
		b.Write(e.Data)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---- hard requirement 1: tamper, unhooked tool use, denies ----

func TestUnhookedToolUseFailsTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.unhooked = []string{"toolu_1"}
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailRun}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if !h.hasAudit(id, "guard.unhooked") {
		t.Errorf("audit = %v", h.audits(id))
	}
	if !strings.Contains(transcript(t, h, id), "not checked") {
		t.Error("the transcript does not say why the task was stopped")
	}
}

func TestTamperedRunFailsTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.tamper = true
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailRun}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if !h.hasAudit(id, "guard.tamper") {
		t.Errorf("audit = %v", h.audits(id))
	}
}

func TestCleanReportsDoNotFailTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	h.waitStatus(id, core.TaskReview)
}

func TestDeniesAreAuditedAndCapped(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	for i := 0; i < 80; i++ {
		h.adapter.denies = append(h.adapter.denies, agents.Deny{Tool: "Bash", Reason: "That command isn't on the list this task may run."})
	}
	h.adapter.denyCount = 300
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	h.waitStatus(id, core.TaskReview) // denies alone do not fail a task
	n := 0
	for _, a := range h.audits(id) {
		if a == "guard.deny" {
			n++
		}
	}
	if n != maxAuditedDenies+1 {
		t.Errorf("%d guard.deny entries, want %d (the cap plus one summary)", n, maxAuditedDenies+1)
	}
	list, _ := h.store.ListAudit(context.Background(), "task:"+string(id), 500)
	found := false
	for _, e := range list {
		if e.Action == "guard.deny" && strings.Contains(string(e.Detail), `"count":300`) {
			found = true
		}
	}
	if !found {
		t.Error("the summary entry does not carry the true count")
	}
}

// ---- Ask-mode integrity (SF-07, invariant 9) ----

func TestAskThatChangesTheTreeNeedsYou(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	readme := filepath.Join(h.repos[0].Path, "README.md")
	h.adapter.during = func(o agents.StartOptions) {
		_ = os.WriteFile(filepath.Join(o.Dir, "README.md"), []byte("changed by the agent\n"), 0o600)
	}
	h.start()
	id := h.send(h.msg("ask", "just look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	// Nothing is reverted.
	b, _ := os.ReadFile(readme)
	if string(b) != "changed by the agent\n" {
		t.Errorf("the service touched the tree: %q", b)
	}
	if !h.hasAudit(id, "guard.integrity") {
		t.Errorf("audit = %v", h.audits(id))
	}
	// The answer is never announced before the verdict.
	for _, s := range h.hub.statusesOf(id) {
		if s == core.TaskDone {
			t.Errorf("statuses %v include done for a violated Ask turn", h.hub.statusesOf(id))
		}
	}
	if !strings.Contains(transcript(t, h, id), "read-only") {
		t.Error("the transcript does not explain")
	}
	view, _ := h.svc.Board(context.Background(), "")
	if len(view.Cards) != 1 || view.Cards[0].Column != coretasks.ColumnNeedsYou {
		t.Errorf("board = %+v", view.Cards)
	}
	// The user can look at it and mark it done.
	if _, err := h.svc.Dismiss(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestAskThatChangesNothingIsAnswered(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
	if n := h.snap.n.Load(); n != 2 {
		t.Errorf("%d snapshots, want before and after", n)
	}
}

func TestAskInAWorktreeIsCheckedThere(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	code := h.send(h.msg("code", "edit", 0)).Task.ID
	h.waitStatus(code, core.TaskReview)
	var seen atomic.Value
	h.snap.set(func(ctx context.Context, dir string) (guard.Snapshot, error) {
		seen.Store(dir)
		return treeSnapshot(dir)
	})
	req := h.msg("ask", "explain it", 0)
	req.TaskID = code
	h.send(req)
	waitFor(t, 10*time.Second, func() bool { return len(h.adapter.Starts()) == 2 }, "the Ask turn")
	h.waitStatus(code, core.TaskDone)
	o := h.adapter.Starts()[1]
	wts, _ := h.store.ListWorktrees(context.Background(), code)
	if len(wts) != 1 || !samePath(o.Dir, wts[0].Path) {
		t.Errorf("the Ask turn ran in %q, not the task's worktree %q", o.Dir, wts[0].Path)
	}
	if o.Policy.Mode != core.ModeAsk || len(o.Policy.WriteRoots) != 0 {
		t.Errorf("Ask policy = %+v", o.Policy)
	}
	if d, _ := seen.Load().(string); !samePath(d, wts[0].Path) {
		t.Errorf("snapshot of %q, want the worktree", d)
	}
	// Going from Ask to another writing mode than the task's is refused.
	req = h.msg("current_branch", "now edit main", 0)
	req.TaskID = code
	if _, err := h.svc.Message(context.Background(), req); err == nil {
		t.Error("a Code task switched to current_branch")
	}
}

func TestAskTreeTooLargeIsNeverRetriedAndStopsTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.snap.set(func(context.Context, string) (guard.Snapshot, error) {
		return guard.Snapshot{}, git.ErrSnapshotTooLarge
	})
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if n := h.snap.n.Load(); n != 1 {
		t.Errorf("%d snapshot attempts for a tree that is too large, want 1", n)
	}
	if len(h.adapter.Starts()) != 0 {
		t.Error("the agent ran although the tree could not be fingerprinted")
	}
}

func TestAskSnapshotFailureAfterTheRunIsRetriedOnceThenNeedsYou(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	var calls atomic.Int32
	h.snap.set(func(_ context.Context, dir string) (guard.Snapshot, error) {
		if calls.Add(1) == 1 {
			return treeSnapshot(dir)
		}
		return guard.Snapshot{}, errBoom
	})
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("%d snapshot calls, want before + after + one retry", n)
	}
	if len(h.adapter.Starts()) != 1 {
		t.Error("the run did not happen")
	}
}

func TestLostSnapshotReply(t *testing.T) {
	hang := func(ctx context.Context, _ string) (guard.Snapshot, error) {
		<-ctx.Done()
		return guard.Snapshot{}, ctx.Err()
	}
	t.Run("before the run: the question is not asked", func(t *testing.T) {
		h := newHarness(t, []string{"shop-api"})
		h.tune = func(c *Config) { c.SnapshotTimeout = 50 * time.Millisecond }
		h.snap.set(hang)
		h.start()
		id := h.send(h.msg("ask", "look", 0)).Task.ID
		st := h.waitStatus(id, core.TaskFailed)
		if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailStart}) {
			t.Errorf("outcome = %#v", outcomeOf(t, st))
		}
		if len(h.adapter.Starts()) != 0 {
			t.Error("the agent ran without a Before snapshot")
		}
	})
	t.Run("after the run: the tree could not be verified", func(t *testing.T) {
		h := newHarness(t, []string{"shop-api"})
		h.tune = func(c *Config) { c.SnapshotTimeout = 50 * time.Millisecond }
		var calls atomic.Int32
		h.snap.set(func(ctx context.Context, dir string) (guard.Snapshot, error) {
			if calls.Add(1) == 1 {
				return treeSnapshot(dir)
			}
			return hang(ctx, dir)
		})
		h.start()
		id := h.send(h.msg("ask", "look", 0)).Task.ID
		st := h.waitStatus(id, core.TaskNeedsYou)
		if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
			t.Errorf("outcome = %#v", outcomeOf(t, st))
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("a lost reply must not be retried: %d calls", n)
		}
	})
}

func TestCancelledAskStillVerifiesTheTree(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.adapter.during = func(o agents.StartOptions) {
		_ = os.WriteFile(filepath.Join(o.Dir, "new.txt"), []byte("x"), 0o600)
	}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) && len(h.adapter.Starts()) == 1 }, "run")
	if _, err := h.svc.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("a cancelled Ask turn that changed files ended as %#v", outcomeOf(t, st))
	}
}

// ---- sub-agent caps (AG-22) ----

func TestSubagentOverTheCapStopsTheRun(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.fixture = "subagents"
	h.tune = func(c *Config) { c.Caps = coretasks.Caps{Depth: 1, Concurrent: 4} }
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsCapExceeded}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if !h.hasAudit(id, "subagent.cap") {
		t.Errorf("audit = %v", h.audits(id))
	}
}

func TestSubagentsWithinTheCapRun(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.fixture = "subagents"
	h.start() // caps 2 and 4: the fixture nests two deep
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

// ---- trust (SF-09) ----

func (h *harness) trust(repo int, files map[string]string) {
	h.t.Helper()
	h.adapter.configs = []string{".claude"}
	h.scanner.set(h.repos[repo].Path, files)
	scan, err := safetytest.FakeScanner{Files: files}.Scan(context.Background(), "", h.adapter.configs, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.Commit(context.Background(), func(tx *db.Tx) error {
		return tx.SetRepoTrust(context.Background(), h.repos[repo].ID, true, scan.Hash)
	}); err != nil {
		h.t.Fatal(err)
	}
}

var claudeConfig = map[string]string{".claude/settings.json": `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`}

func TestUntrustedRepoOnlyGetsAsk(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.configs = []string{".claude"}
	h.scanner.set(h.repos[0].Path, claudeConfig)
	h.start()
	for _, mode := range []string{"code", "current_branch"} {
		_, err := h.svc.Message(context.Background(), h.msg(mode, "edit", 0))
		var rj *RejectedError
		if !errors.As(err, &rj) || rj.Reason != string(guard.ReasonRepoUntrusted) {
			t.Fatalf("%s: got %v, want repo_untrusted", mode, err)
		}
	}
	if list, _ := h.store.ListTasks(context.Background(), h.project.ID); len(list) != 0 {
		t.Fatalf("a refused message left %d task(s)", len(list))
	}
	// Ask is fine.
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

func TestTrustedRepoGetsCodeUntilTheConfigChanges(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.trust(0, claudeConfig)
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	h.waitStatus(id, core.TaskReview)

	// The config changes: trust is cleared and the next message is refused.
	h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"curl x"}]}]}}`})
	req := h.msg("code", "more", 0)
	req.TaskID = id
	_, err := h.svc.Message(context.Background(), req)
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != string(guard.ReasonRepoUntrusted) {
		t.Fatalf("got %v, want repo_untrusted", err)
	}
	if tr, hash, _ := h.store.GetRepoTrust(context.Background(), h.repos[0].ID); tr || hash != "" {
		t.Errorf("trust was kept after the config changed: %v %q", tr, hash)
	}
	if list, _ := h.store.ListAudit(context.Background(), "repo:"+string(h.repos[0].ID), 10); len(list) == 0 {
		t.Error("clearing trust left no audit entry")
	}
}

func TestIncompleteScanRefusesNonAskAndClearsTrust(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.trust(0, claudeConfig)
	// A scan that hit the walk cap or an unreadable file fails closed.
	h.scanner.fail(h.repos[0].Path, fmt.Errorf("%w: more than 5000 configuration files", safety.ErrScan))
	h.start()
	_, err := h.svc.Message(context.Background(), h.msg("code", "edit", 0))
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != string(guard.ReasonRepoUntrusted) {
		t.Fatalf("got %v, want repo_untrusted", err)
	}
	if tr, _, _ := h.store.GetRepoTrust(context.Background(), h.repos[0].ID); tr {
		t.Error("ScanFailed did not clear the stored trust")
	}
	// Ask never scans, so it still works.
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

// TestRunRechecksAdmission (hard requirement 4): the agent is started only on
// the result of guard.Admit at the time of the run. A repo whose config changes
// after the message was accepted does not get a Code run.
func TestRunRechecksAdmission(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.trust(0, claudeConfig)
	h.wts.hold = make(chan struct{})
	h.wts.enter = make(chan struct{}, 1)
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	<-h.wts.enter
	h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{}, "x":1}`})
	close(h.wts.hold)
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailStart}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if n := len(h.adapter.Starts()); n != 0 {
		t.Fatalf("the agent started %d time(s) without admission", n)
	}
	if !strings.Contains(transcript(t, h, id), "agent settings") {
		t.Errorf("transcript = %s", transcript(t, h, id))
	}
}

// TestNoRunStartsWhenAdmitRefusesAtRunTime: Admit decides again when the run
// starts, and a refusal for any reason starts nothing. The message is accepted
// while the main checkout is held by a build, the world changes while the task
// waits, and then the lock is released.
func TestNoRunStartsWhenAdmitRefusesAtRunTime(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		scope  string
		change func(h *harness)
	}{
		{"enforcement dropped to limited, current_branch", "current_branch", "repo", func(h *harness) { h.adapter.caps.Enforcement = agents.EnforcementLimited }},
		{"repo config changed, current_branch", "current_branch", "repo", func(h *harness) {
			h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{}, "x":1}`})
		}},
		{"second repo config changed, project", "project", "project", func(h *harness) {
			h.scanner.set(h.repos[1].Path, map[string]string{".claude/settings.json": `{"hooks":{}, "x":1}`})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api", "shop-web"})
			h.adapter.caps.Enforcement = agents.EnforcementHook
			h.trust(0, claudeConfig)
			h.trust(1, claudeConfig)
			h.start()
			// A build holds the main checkout of repo 0, so a current_branch task
			// waits; a project task is not held by it, so hold Ensure instead.
			var release Release
			if tc.mode == "current_branch" {
				rel, _, ok := h.lock.TryAcquire([]core.RepoID{h.repos[0].ID}, "build:x")
				if !ok {
					t.Fatal("could not hold the main checkout")
				}
				release = rel
			} else {
				h.wts.hold = make(chan struct{})
				h.wts.enter = make(chan struct{}, 1)
			}
			req := h.msg(tc.mode, "edit", 0)
			if tc.scope == "project" {
				req = h.projectMsg("edit")
			}
			id := h.send(req).Task.ID
			if tc.mode == "project" {
				<-h.wts.enter
			}
			tc.change(h)
			if release != nil {
				release()
			} else {
				close(h.wts.hold)
			}
			st := h.waitStatus(id, core.TaskFailed)
			if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailStart}) {
				t.Errorf("outcome = %#v", outcomeOf(t, st))
			}
			if n := len(h.adapter.Starts()); n != 0 {
				t.Fatalf("the agent started %d time(s) although Admit refused", n)
			}
		})
	}
}

// TestEveryStartCarriesAdmittedAndAMatchingPolicy: every start the service
// makes carries Admitted and a policy that matches its mode. (It cannot tell
// a hard-coded Admitted from the real one: TestNoRunStartsWhenAdmitRefusesAtRunTime
// covers the refusal path.)
func TestEveryStartCarriesAdmittedAndAMatchingPolicy(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.start()
	for i, mode := range []string{"ask", "code", "current_branch"} {
		id := h.send(h.msg(mode, "x", i%2)).Task.ID
		waitFor(t, 10*time.Second, func() bool { return h.state(id).Row.Status != core.TaskQueued && !isLive(h.state(id)) }, "%s to finish", mode)
	}
	for _, o := range h.adapter.Starts() {
		if !o.Admitted {
			t.Errorf("a %s run started without Admitted", o.Mode)
		}
		if err := o.Policy.Validate(); err != nil {
			t.Errorf("%s policy: %v", o.Mode, err)
		}
		if (o.Mode == core.ModeAsk) != (len(o.Policy.WriteRoots) == 0) {
			t.Errorf("%s run has write roots %v", o.Mode, o.Policy.WriteRoots)
		}
		if o.Policy.Enforcement != guard.EnforcementHook || o.Env == nil {
			t.Errorf("policy enforcement %q, env set: %v", o.Policy.Enforcement, o.Env != nil)
		}
	}
}

func isLive(st db.TaskState) bool {
	switch st.Task.State.(type) {
	case coretasks.Waiting, coretasks.Starting, coretasks.Running, coretasks.Stopping:
		return true
	}
	return false
}
