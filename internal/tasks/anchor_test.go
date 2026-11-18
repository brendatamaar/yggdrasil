package tasks

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func anchorDraft(h *harness) (core.RequestID, WaitingDraft) {
	repo := h.repos[0].ID
	return coretasks.AnchorRequest(repo), WaitingDraft{Project: h.project.ID, Repo: repo,
		Title: coretasks.AnchorTitle, Brief: coretasks.AnchorBrief, Agent: coretasks.AnchorAgent}
}

// An anchor can never start (StartDraft and the Message path), starts nothing,
// and the other drafts are unaffected by the rule.
func TestStartDraftRefusesAnchorOnly(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	var checks atomic.Int32
	h.adapter.onCheck = func() { checks.Add(1) }
	h.start()
	ctx := context.Background()
	req, d := anchorDraft(h)
	anchor, err := h.svc.SaveWaitingDraft(ctx, req, d)
	if err != nil {
		t.Fatal(err)
	}
	var user *coretasks.UserError
	_, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: anchor, Mode: core.ModeAsk}, "")
	if !errors.As(err, &user) || !strings.Contains(user.Message, "only holds proposals") {
		t.Fatalf("anchor started: %v", err)
	}
	if _, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: anchor, Mode: core.ModeAsk}, coretasks.AnchorAgent); !errors.As(err, &user) {
		t.Fatalf("anchor started with its own agent: %v", err)
	}
	msg := h.msg("ask", "go", 0)
	msg.TaskID = anchor
	if _, err = h.svc.Message(ctx, msg); !errors.As(err, &user) {
		t.Fatalf("anchor started by a message: %v", err)
	}
	turns, _ := h.store.ListTaskTurns(ctx, anchor)
	if len(turns) != 0 || len(h.adapter.Starts()) != 0 || checks.Load() != 0 || h.state(anchor).Row.Status != core.TaskDraft {
		t.Fatalf("the anchor ran: turns=%d starts=%d", len(turns), len(h.adapter.Starts()))
	}

	// Other drafts still start: a waiting draft and an issue draft.
	waiting, err := h.svc.SaveWaitingDraft(ctx, waitingReq, waitingDraft(h))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: waiting, Mode: core.ModeAsk}, ""); err != nil {
		t.Fatalf("waiting draft refused: %v", err)
	}
	imp, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: imp.Task.ID, Mode: core.ModeAsk}, ""); err != nil {
		t.Fatalf("issue draft refused: %v", err)
	}
}

// A repeat of the anchor request returns the same anchor, also after a restart
// and whatever project the caller names (the anchor is per repo).
func TestAnchorReplayIsStableAcrossRestartAndProject(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	req, d := anchorDraft(h)
	first, err := h.svc.SaveWaitingDraft(ctx, req, d)
	if err != nil {
		t.Fatal(err)
	}
	h.restart()
	again, err := h.svc.SaveWaitingDraft(ctx, req, d)
	if err != nil || again != first {
		t.Fatalf("replay: %v %v", again, err)
	}
	if list, err := h.store.ListTasks(ctx, h.project.ID); err != nil || len(list) != 1 {
		t.Fatalf("tasks: %v %v", list, err)
	}
	// Anything other than the fixed anchor payload is refused at the boundary.
	bad := d
	bad.Title = "Workspace actions for shop-api"
	if _, err := h.svc.SaveWaitingDraft(ctx, req, bad); err == nil {
		t.Fatal("anchor with a custom title accepted")
	}
}

// A client-chosen ID can never occupy the anchor's request ID, neither as a
// new task nor as an ordinary waiting draft, so the anchor of a repo is never
// refused with a digest mismatch caused by a user.
func TestReservedRequestIDRefusedForClients(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	reserved := coretasks.AnchorRequest(h.repos[0].ID)

	if _, err := h.svc.SubmitWithRequest(ctx, reserved, h.msg("ask", "hello", 0)); err == nil || errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("reserved id accepted for a task: %v", err)
	}
	d := waitingDraft(h)
	if _, err := h.svc.SaveWaitingDraft(ctx, reserved, d); err == nil {
		t.Fatal("reserved id accepted for an ordinary waiting draft")
	}
	if _, err := h.svc.LookupRequest(ctx, reserved); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a client left a link under the reserved id: %v", err)
	}
	// The anchor is still creatable afterwards.
	req, ad := anchorDraft(h)
	if _, err := h.svc.SaveWaitingDraft(ctx, req, ad); err != nil {
		t.Fatalf("anchor refused after client attempts: %v", err)
	}
}
