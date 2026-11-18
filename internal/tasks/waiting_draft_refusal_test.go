package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// writes counts every durable trace a call could leave: task rows, request
// links and audit rows (all subjects).
type writes struct{ tasks, audit int }

func (h *harness) writes() writes {
	h.t.Helper()
	ctx := context.Background()
	list, err := h.store.ListTasks(ctx, h.project.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	rows, err := h.store.ListAudit(ctx, "", 5000)
	if err != nil {
		h.t.Fatal(err)
	}
	return writes{tasks: len(list), audit: len(rows)}
}

// T3: a refused waiting draft (untracked, unknown, foreign project, reserved
// repo) leaves no task row, no request link and no audit entry, however often
// it is retried, and the answer is the same every time. The refusal does not
// poison the RequestID: once the input is right, the same ID makes exactly one
// draft.
func TestSaveWaitingDraftRefusalLeavesNoTraceAndRetriesAreConsistent(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		arrange func(h *harness, d *WaitingDraft)
		repair  func(h *harness, d *WaitingDraft)
		reason  string
	}{
		{"untracked", func(h *harness, _ *WaitingDraft) { dbtest.UntrackRepo(t, h.store, h.repos[0]) },
			func(h *harness, d *WaitingDraft) { d.Repo = h.repos[1].ID }, "repo_untracked"},
		{"unknown", func(_ *harness, d *WaitingDraft) { d.Repo = "rep_nothere" },
			func(h *harness, d *WaitingDraft) { d.Repo = h.repos[0].ID }, "repo_untracked"},
		{"foreign project", func(h *harness, d *WaitingDraft) {
			d.Project = dbtest.Project(t, h.store, core.Project{Name: "acme-admin", Path: t.TempDir()}).ID
		}, func(h *harness, d *WaitingDraft) { d.Project = h.project.ID }, "repo_untracked"},
		{"reserved", func(h *harness, d *WaitingDraft) { d.Repo = reserveRepo(t, h) },
			func(h *harness, d *WaitingDraft) { d.Repo = h.repos[0].ID }, "repo_not_ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api", "shop-web"})
			h.start()
			d := waitingDraft(h)
			tc.arrange(h, &d)
			before := h.writes()
			for i := 0; i < 3; i++ {
				id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
				if id != "" || !rejectedWith(err, tc.reason) || errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrDigestMismatch) {
					t.Fatalf("attempt %d: %q, %v: want %s every time", i, id, err, tc.reason)
				}
				if got := h.writes(); got != before {
					t.Fatalf("attempt %d: refusal wrote %+v -> %+v", i, before, got)
				}
				if _, err := h.svc.LookupRequest(ctx, waitingReq); !errors.Is(err, db.ErrNotFound) {
					t.Fatalf("attempt %d: refusal linked the request: %v", i, err)
				}
			}
			// A restart does not change the answer or leave anything behind.
			h.restart()
			if _, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d); !rejectedWith(err, tc.reason) {
				t.Fatalf("after restart: %v", err)
			}
			if got := h.writes(); got != before {
				t.Fatalf("restart: %+v -> %+v", before, got)
			}

			// The same RequestID now carries a valid, different payload: one draft.
			tc.repair(h, &d)
			id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
			if err != nil || id == "" {
				t.Fatalf("retry after repair: %q, %v", id, err)
			}
			again, err := h.svc.SaveWaitingDraft(ctx, waitingReq, d)
			if err != nil || again != id {
				t.Fatalf("replay: %q, %v", again, err)
			}
			if got := h.writes(); got.tasks != before.tasks+1 || got.audit != before.audit+1 {
				t.Fatalf("one draft and one audit entry expected: %+v -> %+v", before, got)
			}
			if got := h.audits(id); len(got) != 1 || got[0] != "task.waiting_draft" {
				t.Fatalf("audit of the draft: %v", got)
			}
		})
	}
}

// A refusal after a successful save changes nothing either: the stored draft
// and its audit entry stay exactly as they were.
func TestSaveWaitingDraftRefusalAfterSuccessChangesNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil {
		t.Fatal(err)
	}
	before := h.writes()
	rowBefore := h.state(id).Row
	d := waitingDraft(h)
	d.Repo = "rep_nothere"
	if _, err := h.svc.SaveWaitingDraft(ctx, "req_other", d); !rejectedWith(err, "repo_untracked") {
		t.Fatal(err)
	}
	if got := h.writes(); got != before {
		t.Fatalf("refusal wrote %+v -> %+v", before, got)
	}
	if rowAfter := h.state(id).Row; rowAfter.Status != rowBefore.Status || rowAfter.Prompt != rowBefore.Prompt {
		t.Fatalf("the stored draft changed: %+v -> %+v", rowBefore, rowAfter)
	}
}
