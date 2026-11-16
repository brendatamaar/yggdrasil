package tasks

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// DEFECT (DO NOT MERGE UNTIL FIXED): a replay of an import request returns the
// original draft even after its repo stopped being tracked.
//
// ImportIssueDraft checks the request link (draft.go, "priorDigest" block,
// returning db.ErrConflict, which is then answered with the stored task after
// the transaction) BEFORE it calls coretasks.PlanDraft, which is the only place
// the repo scope (tracked and ready) is refused. So the scope facts are read but
// ignored when the same RequestID and payload arrive again. The T test map
// ("Scope precedes dedup") and the code's own comment require untracked,
// reserved and foreign repos to refuse even when an issue or REQUEST link
// already exists; TestIssueImportUntrackedRepoDoesNotRevealExistingDraft covers
// only a new request ID for the same issue.
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
