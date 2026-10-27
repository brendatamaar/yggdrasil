package proposals

import (
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func TestCreateFillsTheChecksumsACallerLeftOut(t *testing.T) {
	w := newWorld(t)
	w.git.Write(w.wtDir, "a.txt", "content\n")
	wt := coreprop.InWorktree{ID: wtID}
	head := w.head(w.wtDir)
	wantDiff, err := w.git.DiffHash(w.ctx, w.wtDir, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	c := w.create(coreprop.Commit{Where: wt, Paths: []coreprop.RelPath{"a.txt"}, Message: "feat: a"})
	p := w.create(coreprop.Push{Where: wt, Branch: branch, Remote: "origin"})
	m := w.create(coreprop.MergeRequest{Where: wt, Branch: branch, Remote: "origin", TargetBranch: "main", Title: "t"})
	for _, id := range []core.ProposalID{c.ID, p.ID, m.ID} {
		rec, err := w.store.GetProposalRecord(w.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		switch a := rec.Proposal.Action.(type) {
		case coreprop.Commit:
			if a.ExpectHead != head || a.ExpectDiffHash != wantDiff {
				t.Errorf("commit pins %q %q, want %q %q", a.ExpectHead, a.ExpectDiffHash, head, wantDiff)
			}
		case coreprop.Push:
			if a.ExpectHead != head {
				t.Errorf("push pins %q, want %q", a.ExpectHead, head)
			}
		case coreprop.MergeRequest:
			if a.ExpectHead != head {
				t.Errorf("mr pins %q, want %q", a.ExpectHead, head)
			}
		}
	}
}

func TestCreateRefusesWhenTheChecksumsCannotBeComputed(t *testing.T) {
	w := newWorld(t)
	w.git.Fail["head"] = errors.New("cannot read")
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin"}})
	refusedBadFacts(t, err, "push with no readable tip")
}
