package builds_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// This file checks the shell invariants S1, S3 and S4 of docs/design/builds.md
// against the real Service with the fake process runner:
//
//	S1: a step process starts at most once per (build, index, attempt)
//	S3: at most one build is Running at any time (BL-03)
//	S4: no process of a build runs after its build is Finished

type stepID struct {
	build          core.BuildID
	index, attempt int
}

// keyedWriter is the log writer of one step run; it tells the tracker which
// step a process belongs to.
type keyedWriter struct {
	builds.LogWriter
	key stepID
}

type keyedSink struct {
	inner builds.LogSink
}

func (k keyedSink) Open(id core.BuildID, index, attempt int) (builds.LogWriter, error) {
	w, err := k.inner.Open(id, index, attempt)
	if err != nil {
		return nil, err
	}
	return &keyedWriter{LogWriter: w, key: stepID{id, index, attempt}}, nil
}

func (k keyedSink) Read(id core.BuildID, index, attempt int, after int64) ([]byte, int64, int64, error) {
	return k.inner.Read(id, index, attempt, after)
}

// trackedProc is a fake process whose end can be observed.
type trackedProc struct {
	platform.Process
	key  stepID
	dead chan struct{}
}

func (p *trackedProc) isDead() bool {
	select {
	case <-p.dead:
		return true
	default:
		return false
	}
}

// tracker wraps a StartFunc and remembers every process it started.
type tracker struct {
	inner platform.StartFunc
	mu    sync.Mutex
	procs []*trackedProc
}

func (tr *tracker) Start(ctx context.Context, spec platform.Spec) (platform.Process, error) {
	w, ok := spec.Stdout.(*keyedWriter)
	if !ok {
		return nil, fmt.Errorf("tracker: the step log writer is a %T", spec.Stdout)
	}
	p, err := tr.inner(ctx, spec)
	if err != nil {
		return nil, err
	}
	tp := &trackedProc{Process: p, key: w.key, dead: make(chan struct{})}
	go func() { _, _ = p.Wait(); close(tp.dead) }()
	tr.mu.Lock()
	tr.procs = append(tr.procs, tp)
	tr.mu.Unlock()
	return tp, nil
}

func (tr *tracker) snapshot() []*trackedProc {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]*trackedProc(nil), tr.procs...)
}

// starts counts the processes started per step run.
func (tr *tracker) starts() map[stepID]int {
	out := map[stepID]int{}
	for _, p := range tr.snapshot() {
		out[p.key]++
	}
	return out
}

func (tr *tracker) alive(build core.BuildID) []*trackedProc {
	var out []*trackedProc
	for _, p := range tr.snapshot() {
		if p.key.build == build && !p.isDead() {
			out = append(out, p)
		}
	}
	return out
}

// trackedHarness is a harness whose processes and step logs are tracked.
func trackedHarness(t *testing.T) (*harness, *tracker) {
	t.Helper()
	tr := &tracker{}
	h := newHarness(t, func(d *builds.Deps) {
		tr.inner = d.Start
		d.Start = tr.Start
		d.Logs = keyedSink{inner: builds.NewFileLogs(t.TempDir(), safety.NewRedactor(), nil)}
	})
	return h, tr
}

var block = []string{"fake", "block"}

// S4: when a build is Finished, none of its processes is alive, whichever way
// it ended: a cancel, a failing step or a process that dies by itself.
func TestShellS4_NoProcessAfterFinished(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, h *harness, tr *tracker) core.BuildID
	}{
		{"cancelled while a step runs", func(t *testing.T, h *harness, tr *tracker) core.BuildID {
			id := h.enqueue(steps(block, exit(0)))
			h.wait(id, isRunning)
			if err := h.svc.Cancel(context.Background(), id, "user"); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		{"step process dies by itself", func(t *testing.T, h *harness, tr *tracker) core.BuildID {
			id := h.enqueue(steps(block, exit(0)))
			h.wait(id, isRunning)
			deadline := time.Now().Add(5 * time.Second)
			for len(tr.alive(id)) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			for _, p := range tr.alive(id) {
				_ = p.Kill()
			}
			return id
		}},
		{"first step fails, the rest is skipped", func(t *testing.T, h *harness, tr *tracker) core.BuildID {
			return h.enqueue(steps(exit(1), block, block))
		}},
		{"all steps succeed", func(t *testing.T, h *harness, tr *tracker) core.BuildID {
			return h.enqueue(steps(exit(0), exit(0)))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, tr := trackedHarness(t)
			h.start()
			id := c.run(t, h, tr)
			h.finished(id)
			// The build is Finished: the fake process must already be dead.
			if left := tr.alive(id); len(left) != 0 {
				t.Fatalf("build %s is Finished but %d of its processes are alive: %+v", id, len(left), left[0].key)
			}
			for _, p := range tr.snapshot() {
				if p.key.build == id && !p.isDead() {
					t.Fatalf("process %+v alive after Finished", p.key)
				}
			}
		})
	}
}

// S1: a StartStep for a step run that already started is refused; the fake
// runner sees one start only.
func TestShellS1_DuplicateStartStepStartsOnce(t *testing.T) {
	h, tr := trackedHarness(t)
	h.start()
	id := h.enqueue(steps(block))
	h.wait(id, isRunning)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.fake.Started()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := len(h.fake.Started()); n != 1 {
		t.Fatalf("started %d before the duplicate", n)
	}
	cmd := corebuilds.StartStep{BuildID: id, Index: 0, Attempt: 1, RepoID: "rep_a", Target: corebuilds.MainCheckout{}, Argv: block}
	for i := 0; i < 3; i++ {
		if n := h.svc.StartStepAgain(context.Background(), cmd); n != 0 {
			t.Fatalf("a refused start produced %d follow-up inputs", n)
		}
	}
	if n := len(h.fake.Started()); n != 1 {
		t.Fatalf("fake.Started() = %d after duplicate StartStep commands, want 1", n)
	}
	if got := tr.starts()[stepID{id, 0, 1}]; got != 1 {
		t.Fatalf("step run started %d times", got)
	}
	// A different attempt of the same step is a different run and is allowed.
	cmd.Attempt = 2
	h.svc.StartStepAgain(context.Background(), cmd)
	if n := len(h.fake.Started()); n != 2 {
		t.Fatalf("a new attempt did not start: %d", n)
	}
	_ = h.svc.Cancel(context.Background(), id, "user")
	h.finished(id)
}

// Random interleavings of Enqueue, Cancel and process exits (a blocked process
// ending by itself) must keep S1, S3 and S4. The seeds are fixed; the Go
// scheduler adds its own interleavings.
func TestShellChaos_S1S3S4(t *testing.T) {
	seeds := 25
	if testing.Short() {
		seeds = 5
	}
	for seed := 1; seed <= seeds; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			chaos(t, int64(seed))
		})
	}
}

func chaos(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	h, tr := trackedHarness(t)
	h.start()
	ctx := context.Background()
	var ids []core.BuildID

	check := func(when string) {
		t.Helper()
		running, err := h.store.CountRunning(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if running > 1 {
			t.Fatalf("seed %d %s: S3: %d builds running", seed, when, running)
		}
		for k, n := range tr.starts() {
			if n > 1 {
				t.Fatalf("seed %d %s: S1: step run %+v started %d times", seed, when, k, n)
			}
		}
		all, err := h.svc.List(ctx, db.BuildFilter{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range all {
			f, ok := st.(corebuilds.Finished)
			if !ok {
				continue
			}
			if left := tr.alive(f.ID); len(left) != 0 {
				t.Fatalf("seed %d %s: S4: build %s is Finished with %d live processes (step %+v)", seed, when, f.ID, len(left), left[0].key)
			}
		}
	}

	kinds := [][]string{exit(0), exit(1), block, block}
	for op := 0; op < 24; op++ {
		switch n := rng.Intn(10); {
		case n < 4: // enqueue
			var argvs [][]string
			for i := 0; i <= rng.Intn(3); i++ {
				argvs = append(argvs, kinds[rng.Intn(len(kinds))])
			}
			ids = append(ids, h.enqueue(steps(argvs...)))
		case n < 6 && len(ids) > 0: // cancel
			_ = h.svc.Cancel(ctx, ids[rng.Intn(len(ids))], "user")
		case n < 9: // a blocked process ends by itself
			var live []*trackedProc
			for _, p := range tr.snapshot() {
				if !p.isDead() {
					live = append(live, p)
				}
			}
			if len(live) > 0 {
				_ = live[rng.Intn(len(live))].Kill()
			}
		default:
			time.Sleep(time.Duration(rng.Intn(3)) * time.Millisecond)
		}
		check(fmt.Sprintf("after op %d", op))
	}

	// Drain: cancel what is left and let every build finish.
	for _, id := range ids {
		_ = h.svc.Cancel(ctx, id, "user")
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, id := range ids {
		for {
			st, err := h.svc.Get(ctx, id)
			if err == nil && isFinished(st) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("seed %d: build %s never finished: %#v", seed, id, st)
			}
			// Keep ending the processes that a later step started.
			for _, p := range tr.alive(id) {
				_ = p.Kill()
			}
			time.Sleep(time.Millisecond)
		}
	}
	check("at the end")
	for _, id := range ids {
		if left := tr.alive(id); len(left) != 0 {
			t.Fatalf("seed %d: %d processes of finished build %s are alive", seed, len(left), id)
		}
	}
}
