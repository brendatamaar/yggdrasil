package proposalstest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// RequestRig is the subject of ProposalCreatorContract: the request-bound
// creation port the workspace-action service consumes (actions.ProposalCreator),
// either its fake or the real proposals service. The suite sends only what the
// rig builds, so it does not depend on either side's item type.
type RequestRig struct {
	// Create makes one request of n items. A different tag gives different
	// items (same count), so two tags are two different lists.
	Create func(ctx context.Context, req core.RequestID, n int, tag string) ([]core.ProposalID, error)
	// CreateNewRepo sends a request whose only item is an InitRepo.
	CreateNewRepo func(ctx context.Context, req core.RequestID) ([]core.ProposalID, error)
	// Find is the read-only lookup: IDs in item order, or none.
	Find func(ctx context.Context, req core.RequestID) ([]core.ProposalID, error)
	// Pending reports that the proposal exists, is pending and was neither
	// confirmed nor decided.
	Pending func(ctx context.Context, id core.ProposalID) bool
	// Stored counts every proposal the creator persisted.
	Stored func() int
	// Max is the largest request the creator accepts.
	Max int
}

func noneFound(ids []core.ProposalID, err error) bool {
	return errors.Is(err, db.ErrNotFound) || (err == nil && len(ids) == 0)
}

// ProposalCreatorContract states what a ProposalCreator promises: PENDING
// proposals only (invariant 2), one creation per RequestID, a changed list
// refused rather than merged, nothing stored by a refusal, and a read-only Find.
func ProposalCreatorContract(t *testing.T, mk func(t *testing.T) RequestRig) {
	ctx := context.Background()
	t.Run("create makes pending proposals that Find returns in order", func(t *testing.T) {
		r := mk(t)
		ids, err := r.Create(ctx, "req_a", 3, "x")
		if err != nil || len(ids) != 3 {
			t.Fatalf("ids %v, err %v", ids, err)
		}
		seen := map[core.ProposalID]bool{}
		for _, id := range ids {
			if seen[id] || id.Validate() != nil {
				t.Fatalf("ids %v: duplicate or invalid", ids)
			}
			seen[id] = true
			if !r.Pending(ctx, id) {
				t.Fatalf("proposal %s is not pending: creation must not confirm or run anything", id)
			}
		}
		if found, err := r.Find(ctx, "req_a"); err != nil || !slices.Equal(found, ids) {
			t.Fatalf("Find = %v, %v; want %v", found, err, ids)
		}
		if r.Stored() != 3 {
			t.Fatalf("%d stored, want 3", r.Stored())
		}
	})
	t.Run("a repeat returns the first IDs and stores nothing more", func(t *testing.T) {
		r := mk(t)
		first, err := r.Create(ctx, "req_a", 2, "x")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			again, err := r.Create(ctx, "req_a", 2, "x")
			if err != nil || !slices.Equal(again, first) {
				t.Fatalf("repeat %d: %v, %v; want %v", i, again, err, first)
			}
		}
		if r.Stored() != 2 {
			t.Fatalf("%d stored after repeats, want 2", r.Stored())
		}
	})
	t.Run("a changed list under the same request is refused", func(t *testing.T) {
		r := mk(t)
		first, err := r.Create(ctx, "req_a", 2, "x")
		if err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]struct {
			n   int
			tag string
		}{"other content": {2, "y"}, "more items": {3, "x"}, "fewer items": {1, "x"}} {
			if ids, err := r.Create(ctx, "req_a", c.n, c.tag); err == nil || len(ids) != 0 {
				t.Fatalf("%s: %v, %v: want a refusal and no IDs", name, ids, err)
			}
		}
		if found, _ := r.Find(ctx, "req_a"); !slices.Equal(found, first) || r.Stored() != 2 {
			t.Fatalf("a refused change altered the request: %v, %d stored", found, r.Stored())
		}
	})
	t.Run("requests are independent", func(t *testing.T) {
		r := mk(t)
		a, errA := r.Create(ctx, "req_a", 1, "x")
		b, errB := r.Create(ctx, "req_b", 1, "x")
		if errA != nil || errB != nil || a[0] == b[0] || r.Stored() != 2 {
			t.Fatalf("a %v %v, b %v %v, stored %d", a, errA, b, errB, r.Stored())
		}
	})
	t.Run("Find of an unknown request finds nothing and creates nothing", func(t *testing.T) {
		r := mk(t)
		if ids, err := r.Find(ctx, "req_none"); !noneFound(ids, err) {
			t.Fatalf("Find = %v, %v", ids, err)
		}
		if r.Stored() != 0 {
			t.Fatal("Find created a proposal")
		}
	})
	t.Run("refusals store nothing", func(t *testing.T) {
		r := mk(t)
		for name, call := range map[string]func() ([]core.ProposalID, error){
			"no items":      func() ([]core.ProposalID, error) { return r.Create(ctx, "req_r", 0, "x") },
			"empty request": func() ([]core.ProposalID, error) { return r.Create(ctx, "", 1, "x") },
			"too many":      func() ([]core.ProposalID, error) { return r.Create(ctx, "req_big", r.Max+1, "x") },
			"new-repo kind": func() ([]core.ProposalID, error) { return r.CreateNewRepo(ctx, "req_nr") },
		} {
			if ids, err := call(); err == nil || len(ids) != 0 {
				t.Errorf("%s: %v, %v: want a refusal and no IDs", name, ids, err)
			}
		}
		if r.Stored() != 0 {
			t.Fatalf("%d proposals stored by refused requests", r.Stored())
		}
		for _, req := range []core.RequestID{"req_r", "req_big", "req_nr"} {
			if ids, err := r.Find(ctx, req); !noneFound(ids, err) {
				t.Errorf("Find(%s) = %v, %v after a refusal", req, ids, err)
			}
		}
		// A refusal does not poison the request ID.
		if ids, err := r.Create(ctx, "req_nr", 1, "x"); err != nil || len(ids) != 1 {
			t.Fatalf("create after a refusal: %v, %v", ids, err)
		}
	})
}
