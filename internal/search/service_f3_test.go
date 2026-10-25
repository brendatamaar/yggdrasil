package search_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// More repos than slots, the client leaves while repos are still queued: the
// request returns promptly and no worker is left.
func TestSearchClientLeavesWhileReposAreQueued(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 40, nil)
	for _, r := range repos {
		f.Script(r.Path, func(ctx context.Context) (search.GrepResult, error) {
			<-ctx.Done()
			return search.GrepResult{}, ctx.Err()
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := newSvc(t, f, repos, nil).Search(ctx, lit("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 3*time.Second || f.Active.Load() != 0 {
		t.Fatalf("took %v, %d workers active", time.Since(start), f.Active.Load())
	}
}

// A repo that answered before the deadline is not marked timed out when the
// deadline fires while another is still running.
func TestSearchKeepsAnswersGivenBeforeTheDeadline(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 3, map[string]string{"a.txt": "needle\n"})
	f.Script(repos[1].Path, func(ctx context.Context) (search.GrepResult, error) {
		<-ctx.Done()
		return search.GrepResult{}, ctx.Err()
	})
	s := newSvc(t, f, repos, func(d *search.Deps) { d.Deadline = 100 * time.Millisecond })
	st, err := s.Search(context.Background(), lit("needle"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Problems) != 1 || st.Problems[repos[1].ID] == "" {
		t.Fatalf("problems = %v, want only %s", st.Problems, repos[1].ID)
	}
}
