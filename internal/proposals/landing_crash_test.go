package proposals

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Crash point: the merge commit exists but its record (worktree mark + audit)
// was not written. Retrying must finish the job without a second merge: the
// head does not move, the merge commit stays the only one, and the "merged"
// audit entry is written once (docs/design/landing.md, recovery).
func TestMergeLocalCrashAfterRunMergeEndsMergedExactlyOnce(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("c", true)
	base := w.head()

	w.failMarks("merged") // the process "dies" when it records the merge
	v := w.run(w.merge(id, "c", tip), core.ProposalFailed)
	if !v.Retryable {
		t.Fatalf("failure = %+v", v)
	}
	merged := w.head()
	if merged == base {
		t.Fatal("the merge did not happen before the crash point")
	}
	if ps := w.h.Parents(merged); len(ps) != 2 || ps[0] != base || ps[1] != tip {
		t.Fatalf("merge commit parents = %v, want [%s %s]", ps, base, tip)
	}
	if n := countMergedAudits(t, w, v.ID); n != 0 {
		t.Fatalf("%d merged audit entries before the record was written", n)
	}

	w.failMarks("")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got := w.settle(v.ID, core.ProposalExecuted)
	if h := w.head(); h != merged {
		t.Fatalf("the retry moved the head %s -> %s: a second merge ran", merged, h)
	}
	if got.Result == nil || got.Result.CommitID != merged {
		t.Fatalf("result = %+v, want the existing merge commit %s", got.Result, merged)
	}
	if ps := w.h.Parents(merged); len(ps) != 2 || ps[0] != base {
		t.Fatalf("history changed: %v", ps)
	}
	if n := countMergedAudits(t, w, v.ID); n != 1 {
		t.Fatalf("%d merged audit entries after the retry, want exactly 1", n)
	}
	w.res.mu.Lock()
	st := w.res.wts[id].Status
	w.res.mu.Unlock()
	if st != core.WorktreeMerged {
		t.Fatalf("worktree status = %s", st)
	}
	if !w.h.TreeUnchanged(w.main) {
		t.Fatal("the main checkout is not clean")
	}

	// A further retry of an executed proposal changes nothing.
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err == nil && w.head() != merged {
		t.Fatal("a retry of a finished merge moved the head")
	}
}

func countMergedAudits(t *testing.T, w *landWorld, id core.ProposalID) int {
	t.Helper()
	rows, err := w.store.ListAudit(w.ctx, "proposal:"+string(id), 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "landing.merge" && strings.Contains(string(r.Detail), `merged"`) {
			n++
		}
	}
	return n
}
