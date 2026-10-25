package repos_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// retainReader is a StatusReader that also records what the service tells it
// to keep (the optional Retain of the git reader's commit cache).
type retainReader struct {
	mu   sync.Mutex
	kept [][]string
}

func (r *retainReader) Read(context.Context, corerepos.CleanPath) (corerepos.Snapshot, corerepos.ProblemKind, error) {
	return corerepos.Snapshot{Branch: "main"}, "", nil
}

func (r *retainReader) Retain(keep []corerepos.CleanPath) {
	var s []string
	for _, p := range keep {
		s = append(s, p.String())
	}
	slices.Sort(s)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept = append(r.kept, s)
}

func (r *retainReader) last() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.kept) == 0 {
		return nil
	}
	return r.kept[len(r.kept)-1]
}

func TestStatusService_TellsReaderWhichReposAreTracked(t *testing.T) {
	src := &memSource{}
	src.set(repoRow("shop-api"), repoRow("shop-web"))
	rd := &retainReader{}
	svc := repos.NewStatusService(src, rd, time.Now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return svc.Start(gctx) })
	t.Cleanup(func() { cancel(); _ = g.Wait() })

	waitFor := func(n int) []string {
		t.Helper()
		for range 500 {
			if l := rd.last(); len(l) == n {
				return l
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("reader was not told about %d repos; last: %v", n, rd.last())
		return nil
	}
	waitFor(2)

	src.set(repoRow("shop-api")) // shop-web is untracked
	if err := svc.ReloadRepos(ctx); err != nil {
		t.Fatal(err)
	}
	got := waitFor(1)
	if want := []string{string(cpath("shop-api"))}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}
