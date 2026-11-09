package devservers_test

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
)

// A stored server of a repo or worktree that is not tracked any more does not
// exist for a client: ByID (which the log and action endpoints use) says
// NotFound instead of returning the row (invariant 6).
func TestByIDRechecksTracking(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.toRunning(20001)
	id := r.get().ID
	if _, err := r.svc.ByID(ctx, id); err != nil {
		t.Fatalf("tracked: %v", err)
	}
	r.repos.Untrack(r.target)
	_, err := r.svc.ByID(ctx, id)
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("ByID of an untracked target = %v", err)
	}
}
