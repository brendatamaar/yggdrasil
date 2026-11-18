package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

const waitingReq core.RequestID = "req_waiting"

func waitingDraft(h *harness) WaitingDraft {
	return WaitingDraft{Project: h.project.ID, Repo: h.repos[0].ID, Title: "Diagnose shop-api",
		Brief: "Folder unreadable (waiting for a valid working directory). Tracked facts: branch main.", Agent: "mock"}
}

func reserveRepo(t *testing.T, h *harness) core.RepoID {
	t.Helper()
	spec := newrepos.InitSpec{Project: h.project.ID, Repo: "rep_shopnew", Leaf: "shop-new", Branch: newrepos.DefaultBranch,
		README: "# shop-new\n", Ignore: "/node_modules/\n", CommitMessage: "Initial commit"}
	ctx := context.Background()
	if err := h.store.Commit(ctx, func(tx *db.Tx) error {
		return tx.ReserveRepo(ctx, db.ReserveRepo{Operation: "op_shop", Project: h.project.ID, Repo: spec.Repo,
			SetupTask: "tsk_setup", Path: filepath.Join(h.project.Path, "shop-new"), Spec: spec, Agent: "mock", Title: "Set up", Now: h.cfg.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	return spec.Repo
}

func TestSaveWaitingDraftStartsNothing(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	var checks atomic.Int32
	h.adapter.onCheck = func() { checks.Add(1) }
	h.start()
	ctx := context.Background()
	id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil {
		t.Fatal(err)
	}
	st := h.state(id)
	if st.Row.Status != core.TaskDraft || st.Row.Mode != core.ModeAsk || st.Row.Scope != core.ScopeRepo || st.Row.Source != nil ||
		len(st.Row.RepoIDs) != 1 || st.Row.RepoIDs[0] != h.repos[0].ID || st.Row.ProjectID != h.project.ID {
		t.Fatalf("draft: %+v", st.Row)
	}
	turns, err := h.store.ListTaskTurns(ctx, id)
	if err != nil || len(turns) != 0 {
		t.Fatalf("turns: %v %v", turns, err)
	}
	wts, err := h.store.ListWorktrees(ctx, id)
	if err != nil || len(wts) != 0 || len(h.adapter.Starts()) != 0 || checks.Load() != 0 {
		t.Fatalf("execution effects: %v %v starts=%d checks=%d", wts, err, len(h.adapter.Starts()), checks.Load())
	}
	h.restart()
	if got := h.state(id); got.Row.Status != core.TaskDraft || len(h.adapter.Starts()) != 0 {
		t.Fatalf("after restart: %+v", got.Row)
	}
}

func TestSaveWaitingDraftIdempotent(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	first, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil {
		t.Fatal(err)
	}
	h.restart()
	again, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil || again != first {
		t.Fatalf("replay: %v %v", again, err)
	}
	if got := h.audits(first); len(got) != 1 || got[0] != "task.waiting_draft" {
		t.Fatalf("replay wrote audit: %v", got)
	}
	if list, err := h.store.ListTasks(ctx, h.project.ID); err != nil || len(list) != 1 {
		t.Fatalf("tasks: %v %v", list, err)
	}
	if got, err := h.svc.LookupRequest(ctx, waitingReq); err != nil || got != first {
		t.Fatalf("link: %v %v", got, err)
	}
}

func TestSaveWaitingDraftChangedPayloadRefused(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	first, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WaitingDraft){
		"brief": func(d *WaitingDraft) { d.Brief += " more" },
		"title": func(d *WaitingDraft) { d.Title = "Other" },
		"agent": func(d *WaitingDraft) { d.Agent = "other" },
		"model": func(d *WaitingDraft) { d.Model = "model_one" },
	} {
		d := waitingDraft(h)
		edit(&d)
		if _, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if list, err := h.store.ListTasks(ctx, h.project.ID); err != nil || len(list) != 1 || list[0].ID != first {
		t.Fatalf("tasks: %v %v", list, err)
	}
	// An issue import cannot reuse the key either: kinds are separated.
	spec := issueSpec(h)
	spec.RequestID = waitingReq
	if _, err := h.svc.ImportIssueDraft(ctx, spec); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("cross-kind reuse: %v", err)
	}
}

func TestSaveWaitingDraftOnlyTrackedReady(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		arrange func(h *harness, d *WaitingDraft)
		reason  string
	}{
		{"untracked", func(h *harness, _ *WaitingDraft) { dbtest.UntrackRepo(t, h.store, h.repos[0]) }, "repo_untracked"},
		{"unknown", func(_ *harness, d *WaitingDraft) { d.Repo = "rep_nothere" }, "repo_untracked"},
		{"foreign project", func(h *harness, d *WaitingDraft) {
			d.Project = dbtest.Project(t, h.store, core.Project{Name: "other", Path: t.TempDir()}).ID
		}, "repo_untracked"},
		{"reserved", func(h *harness, d *WaitingDraft) { d.Repo = reserveRepo(t, h) }, "repo_not_ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			h.start()
			d := waitingDraft(h)
			tc.arrange(h, &d)
			_, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
			if !rejectedWith(err, tc.reason) || errors.Is(err, db.ErrNotFound) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
			if list, err := h.store.ListTasks(ctx, h.project.ID); err != nil || len(list) != 0 {
				t.Fatalf("refusal wrote a task: %v %v", list, err)
			}
			if _, err := h.svc.LookupRequest(ctx, waitingReq); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("refusal linked the request: %v", err)
			}
		})
	}
	t.Run("scope before replay", func(t *testing.T) {
		h := newHarness(t, []string{"shop-api"})
		h.start()
		if _, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h)); err != nil {
			t.Fatal(err)
		}
		dbtest.UntrackRepo(t, h.store, h.repos[0])
		changed := waitingDraft(h)
		changed.Brief = "x"
		// Same and changed payload both get the scope answer: never the stored
		// task and never the digest error.
		for _, d := range []WaitingDraft{waitingDraft(h), changed} {
			id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
			if !rejectedWith(err, "repo_untracked") || id != "" || errors.Is(err, db.ErrDigestMismatch) {
				t.Fatalf("revealed %q: %v", id, err)
			}
		}
	})
}

func TestSaveWaitingDraftRejectsBadInput(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	for name, edit := range map[string]func(*WaitingDraft){
		"empty brief":   func(d *WaitingDraft) { d.Brief = "  " },
		"huge brief":    func(d *WaitingDraft) { d.Brief = strings.Repeat("a", coretasks.MaxDraftPrompt+1) },
		"empty title":   func(d *WaitingDraft) { d.Title = "" },
		"bad agent":     func(d *WaitingDraft) { d.Agent = "" },
		"nul in brief":  func(d *WaitingDraft) { d.Brief = "a\x00b" },
		"bad repo id":   func(d *WaitingDraft) { d.Repo = "" },
		"title control": func(d *WaitingDraft) { d.Title = "a\nb" },
	} {
		d := waitingDraft(h)
		edit(&d)
		if _, err := h.svc.SaveWaitingDraft(context.Background(), waitingReq, d); !errors.Is(err, coretasks.ErrInvalidDraft) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := h.svc.SaveWaitingDraft(context.Background(), "", waitingDraft(h)); !errors.Is(err, coretasks.ErrInvalidDraft) {
		t.Fatalf("empty request id: %v", err)
	}
}

func TestSaveWaitingDraftRedactsBriefAndAudit(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	d := waitingDraft(h)
	d.Brief = "Remote said token " + token + " is bad."
	d.Title = "Diagnose " + token
	id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
	if err != nil {
		t.Fatal(err)
	}
	st := h.state(id)
	if strings.Contains(st.Row.Prompt, token) || strings.Contains(st.Row.Title, token) || !strings.Contains(st.Row.Prompt, "is bad.") {
		t.Fatalf("stored: %q / %q", st.Row.Title, st.Row.Prompt)
	}
	entries, err := h.store.ListAudit(ctx, "task:"+string(id), 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "task.waiting_draft" || strings.Contains(string(entries[0].Detail), token) {
		t.Fatalf("audit: %+v %v", entries, err)
	}
	// The digest is over the caller's payload, so the same request replays.
	if again, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d); err != nil || again != id {
		t.Fatalf("replay with redacted text: %v %v", again, err)
	}
}

func TestSaveWaitingDraftAuditFailureRollsBackAllWrites(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	imported, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := h.store.ListAudit(ctx, "task:"+string(imported.Task.ID), 10)
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit: %v %v", entries, err)
	}
	originalID := h.svc.c.NewID
	h.svc.c.NewID = func(prefix string) string {
		if prefix == "aud" {
			return string(entries[0].ID) // duplicate primary key: the audit write fails
		}
		return originalID(prefix)
	}
	if _, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h)); err == nil {
		t.Fatal("audit failure was ignored")
	}
	if _, err := h.svc.LookupRequest(ctx, waitingReq); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("partial link: %v", err)
	}
	if list, err := h.store.ListTasks(ctx, h.project.ID); err != nil || len(list) != 1 {
		t.Fatalf("partial task: %v %v", list, err)
	}
}

// Starting the draft is the ordinary path: the save does not bypass it. While
// the folder is still unreadable the run ends without an agent call; an
// untrusted repo is refused in a writing mode; a readable folder goes through
// the usual queue in Ask mode.
func TestWaitingDraftStartGoesThroughAdmission(t *testing.T) {
	ctx := context.Background()
	t.Run("still unreadable", func(t *testing.T) {
		h := newHarness(t, []string{"shop-api"})
		h.start()
		id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
		if err != nil {
			t.Fatal(err)
		}
		h.snap.set(func(context.Context, string) (guard.Snapshot, error) {
			return guard.Snapshot{}, errors.New("folder unreadable")
		})
		if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "mock"); err != nil {
			t.Fatalf("start: %v", err)
		}
		settled := func() bool {
			st := h.state(id).Row.Status
			return st != core.TaskDraft && st != core.TaskQueued && st != core.TaskRunning
		}
		waitFor(t, 10*time.Second, settled, "unreadable start to settle")
		if st := h.state(id).Row.Status; st == core.TaskDone || st == core.TaskReview {
			t.Fatalf("unreadable folder ran as success: %s", st)
		}
		if n := len(h.adapter.Starts()); n != 0 {
			t.Fatalf("agent started in an unreadable folder: %d", n)
		}
	})
	t.Run("untrusted repo refused in a writing mode", func(t *testing.T) {
		h := trustHarness(t, claudeConfig)
		h.adapter.caps.Enforcement = agents.EnforcementHook
		id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeCurrentBranch, OptIn: true}, "mock")
		var r *RejectedError
		if !errors.As(err, &r) || r.Reason != "repo_untrusted" || len(h.adapter.Starts()) != 0 {
			t.Fatalf("untrusted repo admitted: %v", err)
		}
	})
	t.Run("readable folder uses the queue", func(t *testing.T) {
		h := newHarness(t, []string{"shop-api"})
		h.start()
		id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
		if err != nil {
			t.Fatal(err)
		}
		res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "mock")
		if err != nil || res.Task.ID != id {
			t.Fatalf("start: %+v %v", res, err)
		}
		h.waitStatus(id, core.TaskDone)
		turns, err := h.store.ListTaskTurns(ctx, id)
		if err != nil || len(turns) != 1 || turns[0].Mode != core.ModeAsk || len(h.adapter.Starts()) != 1 {
			t.Fatalf("turns: %v %v", turns, err)
		}
		if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "mock"); err == nil {
			t.Fatal("a started draft started again")
		}
	})
}
