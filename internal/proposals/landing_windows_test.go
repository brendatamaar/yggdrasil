//go:build windows

package proposals

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// With the real platform on Windows, an open file inside the worktree stops the
// rename: the run fails retryable, changes nothing, kills nothing, and Retry
// succeeds once the handle is closed (RV-06, PL-03).
func TestRemoveWorktreeRealLockedFileThenRetrySucceeds(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("lk", true)
	held, err := os.Open(filepath.Join(dir, "lk.txt"))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = held.Close()
		}
	})
	got := w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if !got.Retryable || got.FailKind != string(coreprop.FailExec) {
		t.Fatalf("failure = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "lk.txt")); err != nil {
		t.Fatalf("the worktree changed: %v", err)
	}
	if len(w.marks.entries()) != 0 {
		t.Fatalf("marks = %v", w.marks.entries())
	}
	_ = held.Close()
	closed = true
	if _, err := w.svc.Retry(w.ctx, got.ID, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g, _ := w.svc.Get(w.ctx, got.ID); return g.Status == core.ProposalExecuted })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the worktree is still there after the retry")
	}
}
