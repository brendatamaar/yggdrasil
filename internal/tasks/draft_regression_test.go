package tasks

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// Regression: a replay of an import request once returned the original draft
// even after its repo stopped being tracked, because the request link was
// checked before the scope facts. Scope now precedes a replay (and the digest
// check), so untracked, reserved and foreign repos refuse even when an issue or
// request link already exists.
func TestIssueImportReplayOfRequestRefusesWhenRepoNoLongerTracked(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	first, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	dbtest.UntrackRepo(t, h.store, h.repos[0])
	res, err := h.svc.ImportIssueDraft(ctx, issueSpec(h)) // same request ID and payload
	if !rejectedWith(err, "repo_untracked") || res.Task.ID != "" {
		t.Fatalf("a replay revealed %s outside the project scope: %+v %v", first.Task.ID, res, err)
	}
}
