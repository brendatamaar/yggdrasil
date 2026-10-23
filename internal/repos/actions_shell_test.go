package repos_test

import (
	"context"
	"sync"
	"testing"
	"time"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// lingerGit is a runner whose commands need a moment to end after their
// context is cancelled, like a git process being torn down. It counts the
// commands that began and the ones still running.
type lingerGit struct {
	*scriptGit
	linger time.Duration

	mu       sync.Mutex
	entered  int
	inflight int
}

func (g *lingerGit) begin() {
	g.mu.Lock()
	g.entered++
	g.inflight++
	g.mu.Unlock()
}

func (g *lingerGit) end() {
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
}

func (g *lingerGit) counts() (entered, inflight int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.entered, g.inflight
}

func (g *lingerGit) Fetch(ctx context.Context, dir, remote string) error {
	g.begin()
	defer g.end()
	err := g.scriptGit.Fetch(ctx, dir, remote)
	time.Sleep(g.linger)
	return err
}

func (g *lingerGit) PullFFOnly(ctx context.Context, dir string) error {
	g.begin()
	defer g.end()
	err := g.scriptGit.PullFFOnly(ctx, dir)
	time.Sleep(g.linger)
	return err
}

func (g *lingerGit) Checkout(ctx context.Context, dir, branch string) error {
	g.begin()
	defer g.end()
	err := g.scriptGit.Checkout(ctx, dir, branch)
	time.Sleep(g.linger)
	return err
}

// S3: when the server's context ends, Run returns only after every running
// command has returned, and no queued command starts afterwards.
func TestActionsShellS3_RunWaitsForCommandsAndStartsNoMore(t *testing.T) {
	var lg *lingerGit
	e := newActEnv(t, actOpts{repos: 5, max: 2, wrap: func(s *scriptGit) repos.GitRunner {
		lg = &lingerGit{scriptGit: s, linger: 150 * time.Millisecond}
		return lg
	}})
	e.git.gate = make(chan struct{}) // commands run until their context ends
	if _, err := e.start("fetch", "", e.ids...); err != nil {
		t.Fatal(err)
	}
	<-e.git.started
	<-e.git.started // the two slots are in use, three repos wait

	e.stop()
	select {
	case <-e.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	entered, inflight := lg.counts()
	if inflight != 0 {
		t.Fatalf("Run returned while %d git commands were still running", inflight)
	}
	if entered != 2 {
		t.Fatalf("%d git commands started, want the 2 that ran when shutdown began", entered)
	}
	// Nothing starts later either, and a new batch is refused.
	time.Sleep(250 * time.Millisecond)
	if again, _ := lg.counts(); again != entered {
		t.Fatalf("a command started after Run returned: %d -> %d", entered, again)
	}
	req, err := repos.ParseBatchRequest("fetch", []string{string(e.ids[0])}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Start(context.Background(), req, "user"); err == nil {
		t.Fatal("a batch was accepted after shutdown")
	}
	if again, _ := lg.counts(); again != entered {
		t.Fatalf("a command started for a batch after shutdown: %d -> %d", entered, again)
	}
}

// A result that arrives twice, or for an attempt that is not current, changes
// nothing: one audit entry, one status refresh, one finished item.
func TestActionsShellDuplicateResultDelivery(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 1})
	e.git.gate = make(chan struct{})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	<-e.git.started
	repo := e.ids[0]
	res := corerepos.ActionDone{Batch: id, Repo: repo, Attempt: 1, Outcome: corerepos.Fetched{}, Now: time.Now()}

	stale := res
	stale.Attempt = 9
	if err := e.svc.Deliver(stale); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.Get(id); v.Items[0].State != "running" {
		t.Fatalf("a result for another attempt ended the item: %v", states(v))
	}
	if err := e.svc.Deliver(res); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Deliver(res); err != nil {
		t.Fatal(err)
	}
	// The real command ends as well: a third delivery of the same result.
	close(e.git.gate)
	e.stop()
	select {
	case <-e.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	v, ok := e.svc.Get(id)
	if !ok || v.Items[0].State != "done" || v.Items[0].Outcome != "fetched" {
		t.Fatalf("batch = %+v", v)
	}
	fetches := 0
	for _, a := range e.audits("repo:" + string(repo)) {
		if a == "git.fetch" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Fatalf("%d git.fetch audit entries, want exactly one", fetches)
	}
	if got := e.audits("batch:" + string(id)); len(got) != 1 {
		t.Fatalf("batch audit entries = %v, want exactly the start entry", got)
	}
	e.rec.mu.Lock()
	n := 0
	for _, r := range e.rec.ids {
		if r == repo {
			n++
		}
	}
	e.rec.mu.Unlock()
	if n != 1 {
		t.Fatalf("status refreshed %d times for one finished command", n)
	}
}
