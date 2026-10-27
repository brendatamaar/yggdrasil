package proposals

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// The repo row's stored default branch counts, whatever the clone knows.
func TestPushToTheStoredDefaultBranchFails(t *testing.T) {
	w := newWorld(t)
	w.res.mu.Lock()
	w.res.repo.DefaultBranch = "develop"
	w.res.mu.Unlock()
	w.switchBranch("develop")
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: "develop", Remote: "origin", ExpectHead: w.head(w.wtDir)})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || got.Retryable || len(w.git.pushes) != 0 {
		t.Fatalf("failure = %+v, pushes %d", got, len(w.git.pushes))
	}
}
