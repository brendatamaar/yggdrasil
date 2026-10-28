package proposals

import (
	"slices"
	"testing"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func TestViewCarriesTheRepoNameAndTheRemoteHost(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.Push{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", ExpectHead: w.head(w.wtDir)})
	if v.RepoName != "shop-api" || v.Remote != "gitlab.example.test" {
		t.Fatalf("view = repo %q remote %q", v.RepoName, v.Remote)
	}
	if !slices.Contains(v.Lines, "Remote: gitlab.example.test") {
		t.Fatalf("lines = %q", v.Lines)
	}
	got, err := w.svc.Get(w.ctx, v.ID)
	if err != nil || got.Remote != v.Remote || got.RepoName != v.RepoName {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// The host line is not part of what is hashed: the hash is of the action.
	c := w.create(w.commitAction("a.txt", "feat: a"))
	if c.Remote != "" || c.RepoName != "shop-api" {
		t.Fatalf("commit view = %+v", c)
	}
}
