package repos

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

func TestGitReader_CachedCommitDoesNotShareParents(t *testing.T) {
	r := NewGitReader(nil)
	src := git.Commit{Hash: "h", Parents: []string{"p1", "p2"}}
	r.remember("d", "h", src)
	src.Parents[0] = "changed"
	got, ok := r.cached("d", "h")
	if !ok || got.Parents[0] != "p1" {
		t.Fatalf("cache shares the stored slice: %+v", got)
	}
	got.Parents[1] = "changed"
	again, _ := r.cached("d", "h")
	if again.Parents[1] != "p2" {
		t.Fatalf("returned slice is shared with the cache: %+v", again)
	}
}
