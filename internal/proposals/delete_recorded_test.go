package proposals

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// A file that was already in _to_delete before the proposal must not make a
// missing file look moved.
func TestDeleteDoesNotCountAPreExistingFileInToDelete(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "_to_delete/gone.txt", "left by someone else\n")
	v := w.create(coreprop.DeleteFiles{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{"gone.txt"}})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailExec) {
		t.Fatalf("failure = %+v", got)
	}
}

// A retry after a partial move counts what this proposal moved.
func TestDeleteRetryCountsTheFilesThisProposalMoved(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "a.txt", "a\n")
	v := w.create(coreprop.DeleteFiles{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{"a.txt", "b.txt"}})
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalFailed) // b.txt does not exist
	writeFile(t, w.wtDir, "b.txt", "b\n")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(v.ID, core.ProposalExecuted)
	if done.Result == nil || len(done.Result.MovedPaths) != 2 {
		t.Fatalf("result = %+v", done.Result)
	}
}
