package proposals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func TestCreateRefusesADirectoryPath(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(filepath.Join(w.wtDir, "src", "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	wt := coreprop.InWorktree{ID: wtID}
	head := w.head(w.wtDir)
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.Commit{Where: wt, Paths: []coreprop.RelPath{"src"}, Message: "m", ExpectHead: head, ExpectDiffHash: "abcdef0"}})
	refusedBadFacts(t, err, "commit of a directory")
	_, err = w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.DeleteFiles{Where: wt, Paths: []coreprop.RelPath{"src/pkg"}}})
	refusedBadFacts(t, err, "delete of a directory")
}

func TestCommitFailsWhenSomethingElseIsStaged(t *testing.T) {
	for name, extra := range map[string]string{"unrelated file": "other.txt", "protected file": ".claude/settings.json"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			v := w.create(w.commitAction("a.txt", "feat: a"))
			w.git.extraStaged = []string{extra}
			w.confirm(v)
			got := w.waitFor(v.ID, core.ProposalFailed)
			if got.FailKind != string(coreprop.FailPrecondition) || got.Retryable || w.git.commitCount() != 0 {
				t.Fatalf("failure = %+v, commits %d", got, w.git.commitCount())
			}
		})
	}
}
