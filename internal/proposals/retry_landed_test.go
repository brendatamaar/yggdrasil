package proposals

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func TestRetryOfACommitThatLandedIsExecutedNotStale(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	w.git.loseCommitResult = true
	w.confirm(v)
	failed := w.waitFor(v.ID, core.ProposalFailed)
	if failed.FailKind != string(coreprop.FailInterrupted) || !failed.Retryable {
		t.Fatalf("failure = %+v", failed)
	}
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got := w.waitFor(v.ID, core.ProposalExecuted)
	if got.Result == nil || got.Result.CommitID == "" || w.git.commitCount() != 1 {
		t.Fatalf("result = %+v, commits %d", got.Result, w.git.commitCount())
	}
}

func TestAgentFileThatAlreadyHasTheProposedContentIsExecutedNotStale(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.AgentFile{Where: coreprop.InWorktree{ID: wtID}, Path: "AGENTS.md", Content: "proposed\n"})
	// An earlier attempt wrote the file but its result was lost.
	writeFile(t, w.wtDir, "AGENTS.md", "proposed\n")
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalExecuted)
}
