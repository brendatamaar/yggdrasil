package proposalstest

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// DraftRig is the subject of FolderDraftsContract: the port that saves a
// waiting diagnosis draft for a repo whose folder cannot be read
// (actions.FolderDrafts), either its fake or tasks.Service.SaveWaitingDraft.
type DraftRig struct {
	// Save stores the draft of a request. Variant 0 is the base payload; 1, 2
	// and 3 change the brief, the title and the agent.
	Save func(ctx context.Context, req core.RequestID, variant int) (core.TaskID, error)
	// SaveForeign saves for a repo the project does not own.
	SaveForeign func(ctx context.Context, req core.RequestID) (core.TaskID, error)
	// Lookup is the read-only reconciliation of a request (ErrNotFound if none).
	Lookup func(ctx context.Context, req core.RequestID) (core.TaskID, error)
	// Untrack stops tracking the draft's repo.
	Untrack func()
	// Draft returns the stored task's status and mode.
	Draft func(ctx context.Context, id core.TaskID) (core.TaskStatus, core.TaskMode, error)
	// Tasks counts persisted tasks; Starts counts agent starts or turns.
	Tasks  func() int
	Starts func() int
}

// FolderDraftsContract states the promises of a waiting draft: one Ask-mode
// draft per RequestID, no agent run, a changed payload refused, tracked scope
// answered before any replay, and refusals that store nothing.
func FolderDraftsContract(t *testing.T, mk func(t *testing.T) DraftRig) {
	ctx := context.Background()
	t.Run("saves one Ask draft and starts nothing", func(t *testing.T) {
		r := mk(t)
		id, err := r.Save(ctx, "req_w", 0)
		if err != nil || id == "" {
			t.Fatalf("%q, %v", id, err)
		}
		st, mode, err := r.Draft(ctx, id)
		if err != nil || st != core.TaskDraft || mode != core.ModeAsk {
			t.Fatalf("draft is %s/%s, %v: want draft/ask", st, mode, err)
		}
		if r.Tasks() != 1 || r.Starts() != 0 {
			t.Fatalf("%d tasks, %d starts", r.Tasks(), r.Starts())
		}
		if got, err := r.Lookup(ctx, "req_w"); err != nil || got != id {
			t.Fatalf("Lookup = %q, %v; want %q", got, err, id)
		}
	})
	t.Run("a repeat returns the same task", func(t *testing.T) {
		r := mk(t)
		first, err := r.Save(ctx, "req_w", 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if again, err := r.Save(ctx, "req_w", 0); err != nil || again != first {
				t.Fatalf("repeat %d: %q, %v", i, again, err)
			}
		}
		if r.Tasks() != 1 {
			t.Fatalf("%d tasks, want 1", r.Tasks())
		}
	})
	t.Run("a changed payload under the same request is refused", func(t *testing.T) {
		r := mk(t)
		if _, err := r.Save(ctx, "req_w", 0); err != nil {
			t.Fatal(err)
		}
		for v := 1; v <= 3; v++ {
			if id, err := r.Save(ctx, "req_w", v); !errors.Is(err, db.ErrDigestMismatch) || id != "" {
				t.Fatalf("variant %d: %q, %v: want a digest mismatch", v, id, err)
			}
		}
		if r.Tasks() != 1 {
			t.Fatalf("%d tasks, want 1", r.Tasks())
		}
	})
	t.Run("Lookup of an unknown request is not found", func(t *testing.T) {
		r := mk(t)
		if id, err := r.Lookup(ctx, "req_none"); !errors.Is(err, db.ErrNotFound) || id != "" {
			t.Fatalf("%q, %v", id, err)
		}
	})
	t.Run("a foreign or untracked scope is refused and stores nothing", func(t *testing.T) {
		r := mk(t)
		if id, err := r.SaveForeign(ctx, "req_f"); err == nil || id != "" {
			t.Fatalf("foreign: %q, %v", id, err)
		}
		r.Untrack()
		if id, err := r.Save(ctx, "req_u", 0); err == nil || id != "" {
			t.Fatalf("untracked: %q, %v", id, err)
		}
		if r.Tasks() != 0 || r.Starts() != 0 {
			t.Fatalf("%d tasks, %d starts after refusals", r.Tasks(), r.Starts())
		}
		for _, req := range []core.RequestID{"req_f", "req_u"} {
			if _, err := r.Lookup(ctx, req); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("Lookup(%s) after a refusal: %v", req, err)
			}
		}
	})
	t.Run("scope is answered before the stored draft is revealed", func(t *testing.T) {
		r := mk(t)
		first, err := r.Save(ctx, "req_w", 0)
		if err != nil {
			t.Fatal(err)
		}
		r.Untrack()
		for _, v := range []int{0, 1} {
			if id, err := r.Save(ctx, "req_w", v); err == nil || id != "" || id == first {
				t.Fatalf("variant %d after untracking: %q, %v: the stored task must not be revealed", v, id, err)
			}
		}
	})
}
