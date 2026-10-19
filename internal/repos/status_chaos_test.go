package repos_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

// TestStatusS1_NeverMoreThanMaxReadsAtOnce is shell invariant S1 of
// docs/design/repo-status.md, with reads held open so the count is exact:
// exactly Max start, no more get going while they are held, and the rest run
// as slots free up.
func TestStatusS1_NeverMoreThanMaxReadsAtOnce(t *testing.T) {
	const total, maxReads = 12, 3
	var names []string
	for i := range total {
		names = append(names, fmt.Sprintf("r%d", i))
	}
	gate := make(chan struct{})
	h := start(t, names, nil, repos.WithMaxReads(maxReads), repos.WithReadTimeout(time.Minute))
	for _, n := range names {
		h.reader.Script(cpath(n), repostest.FakeResult{Snap: corerepos.Snapshot{Branch: "main"}, Gate: gate})
	}
	if err := h.svc.Refresh(t.Context(), nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Max reads to be running", func() bool { return h.reader.Active() >= maxReads })
	// Give a broken bound time to start more.
	time.Sleep(150 * time.Millisecond)
	if got := h.reader.Active(); got != maxReads {
		t.Errorf("%d reads running while all are held, want exactly %d", got, maxReads)
	}
	close(gate)
	eventually(t, "all repos fresh", h.allView(t, total, isFresh))
	if got := h.reader.MaxActive(); got > maxReads {
		t.Errorf("MaxActive = %d, want at most %d", got, maxReads)
	}
}

// TestStatusS2_CancelStopsBlockedReads is shell invariant S2: with every slot
// held by a read that never ends on its own, cancelling the service context
// ends the reads and Start returns.
func TestStatusS2_CancelStopsBlockedReads(t *testing.T) {
	names := []string{"api", "web", "admin", "docs", "libs"}
	h := start(t, names, nil, repos.WithMaxReads(2), repos.WithReadTimeout(time.Hour))
	for _, n := range names {
		h.reader.Script(cpath(n), repostest.FakeResult{Gate: make(chan struct{})}) // never closed
	}
	if err := h.svc.Refresh(t.Context(), nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reads to be blocked", func() bool { return h.reader.Active() == 2 })
	h.stop(t) // fails if Start does not return within 5 s
	if got := h.reader.Active(); got != 0 {
		t.Errorf("%d reads still running after the service stopped", got)
	}
}

// blockingGit is a gitReads that returns only when its context ends.
type blockingGit struct{ started chan struct{} }

func (b blockingGit) Status(ctx context.Context, _ string) (git.Status, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	return git.Status{}, ctx.Err()
}

func (b blockingGit) Log(ctx context.Context, _ string, _ int) ([]git.Commit, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A read through GitReader hands its context to git (which runs through
// platform.Start, so the process dies with the context): cancel it and the
// read returns instead of waiting for git.
func TestStatusS2_GitReaderReturnsWhenCancelled(t *testing.T) {
	g := blockingGit{started: make(chan struct{}, 1)}
	r := repos.NewGitReader(g)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, err := r.Read(ctx, corerepos.CleanPath(t.TempDir()))
		done <- err
	}()
	<-g.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Read = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after its context was cancelled")
	}
}
