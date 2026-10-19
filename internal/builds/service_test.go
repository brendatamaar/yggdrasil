package builds_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/buildstest"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/node"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const sentinel = "sentinel-secret-value-4711"

type recPub struct {
	mu      sync.Mutex
	changed []string
	lines   []string
}

func (p *recPub) PublishBuildChanged(id core.BuildID, st core.BuildStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.changed = append(p.changed, string(id)+":"+string(st))
}

func (p *recPub) PublishBuildLog(_ core.BuildID, step, _ int, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, fmt.Sprintf("%d:%s", step, line))
}

type recStatus struct {
	mu    sync.Mutex
	calls [][]core.RepoID
}

func (r *recStatus) Refresh(_ context.Context, ids []core.RepoID, _ corerepos.RefreshReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ids)
	return nil
}

type locator struct {
	repos     map[core.RepoID]string
	worktrees map[core.WorktreeID]string
}

func (l locator) RepoDir(_ context.Context, id core.RepoID) (string, error) {
	if d, ok := l.repos[id]; ok {
		return d, nil
	}
	return "", errors.New("unknown repo")
}

func (l locator) WorktreeDir(_ context.Context, id core.WorktreeID) (string, error) {
	if d, ok := l.worktrees[id]; ok {
		return d, nil
	}
	return "", errors.New("unknown worktree")
}

func (l locator) Known(context.Context) (builds.Known, error) {
	k := builds.Known{Repos: map[core.RepoID]struct{}{}, Worktrees: map[core.WorktreeID]core.RepoID{}}
	for id := range l.repos {
		k.Repos[id] = struct{}{}
	}
	return k, nil
}

type harness struct {
	t      *testing.T
	svc    *builds.Service
	store  *buildstest.Fake
	fake   *platformtest.Fake
	pub    *recPub
	status *recStatus
	logs   string
	dirs   map[core.RepoID]string
	stop   context.CancelFunc
	done   chan error
}

func newHarness(t *testing.T, mod ...func(*builds.Deps)) *harness {
	t.Helper()
	h := &harness{t: t, fake: &platformtest.Fake{}, pub: &recPub{}, status: &recStatus{}, logs: t.TempDir(), dirs: map[core.RepoID]string{}}
	for _, r := range []core.RepoID{"rep_a", "rep_b"} {
		h.dirs[r] = filepath.Join(t.TempDir(), "my "+string(r))
		if err := os.MkdirAll(h.dirs[r], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.store = buildstest.NewFake(map[core.RepoID]core.ProjectID{"rep_a": "prj_1", "rep_b": "prj_1"})
	var n atomic.Int64
	red := safety.NewRedactor(sentinel)
	deps := builds.Deps{
		Store: h.store, Start: h.fake.Start, Locator: locator{repos: h.dirs, worktrees: map[core.WorktreeID]string{}},
		Env:     safety.NewEnvBuilder(),
		Resolve: func(name, _ string, _ []string) (string, error) { return name, nil },
		Clock: func() time.Time {
			return time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(n.Add(1)) * time.Second)
		},
		IDs:      func(prefix string) string { return fmt.Sprintf("%s_%d", prefix, n.Add(1)) },
		Redactor: red, Publisher: h.pub, Status: h.status, DataDir: h.logs,
		Toolchains: []builds.Toolchain{node.New()},
		OpenFS: func(string) fs.FS {
			return fstest.MapFS{"package.json": {Data: []byte(`{"scripts":{}}`)}, "dist/app.js": {Data: []byte("x")}, "src/a.js": {Data: []byte("y")}}
		},
	}
	for _, m := range mod {
		m(&deps)
	}
	svc, err := builds.NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.svc.Start(ctx) }()
	h.t.Cleanup(h.shutdown)
}

func (h *harness) shutdown() {
	if h.stop == nil {
		return
	}
	h.stop()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.t.Error("service did not stop")
	}
	h.stop = nil
}

func steps(argvs ...[]string) corebuilds.Spec {
	s := corebuilds.Spec{Target: corebuilds.MainCheckout{}}
	for i, a := range argvs {
		repo := core.RepoID("rep_a")
		if i%2 == 1 {
			repo = "rep_b"
		}
		s.Steps = append(s.Steps, corebuilds.StepSpec{RepoID: repo, Name: fmt.Sprintf("step %d", i), Argv: a})
	}
	return s
}

func exit(code int) []string { return []string{"fake", "exit", fmt.Sprint(code)} }

func (h *harness) enqueue(spec corebuilds.Spec) core.BuildID {
	h.t.Helper()
	id, err := h.svc.Enqueue(context.Background(), spec, "user")
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) wait(id core.BuildID, cond func(corebuilds.State) bool) corebuilds.State {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := h.svc.Get(context.Background(), id)
		if err == nil && cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timeout waiting for %s; state %#v err %v", id, st, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func isFinished(st corebuilds.State) bool { _, ok := st.(corebuilds.Finished); return ok }
func isRunning(st corebuilds.State) bool  { _, ok := st.(corebuilds.Running); return ok }
func isQueued(st corebuilds.State) bool   { _, ok := st.(corebuilds.Queued); return ok }

func (h *harness) finished(id core.BuildID) corebuilds.Finished {
	h.t.Helper()
	return h.wait(id, isFinished).(corebuilds.Finished)
}

func TestHappyPathMultiStep(t *testing.T) {
	h := newHarness(t)
	h.start()
	var started []builds.BuildStarted
	_ = started
	id := h.enqueue(steps(exit(0), []string{"fake", "print", "hello"}, exit(0)))
	f := h.finished(id)
	if _, ok := f.Result.(corebuilds.ResultSucceeded); !ok {
		t.Fatalf("result %#v", f.Result)
	}
	h.wait(id, func(s corebuilds.State) bool { f, ok := s.(corebuilds.Finished); return ok && f.Artifacts != nil })
	f = h.finished(id)
	if len(f.Artifacts) != 2 || f.Artifacts[0].Path != "dist/app.js" || f.Artifacts[0].RepoID == f.Artifacts[1].RepoID {
		t.Errorf("artifacts %+v", f.Artifacts)
	}
	for i, s := range f.Steps {
		if d, ok := s.(corebuilds.StepDone); !ok || d.Attempt != 1 {
			t.Errorf("step %d: %#v", i, s)
		}
	}
	got := h.fake.Started()
	if len(got) != 3 {
		t.Fatalf("started %d processes", len(got))
	}
	for i, sp := range got {
		if !slices.Contains(sp.Env, "CI=true") {
			t.Errorf("step %d env has no CI=true", i)
		}
		if want := h.dirs[map[bool]core.RepoID{true: "rep_a", false: "rep_b"}[i%2 == 0]]; sp.Dir != want {
			t.Errorf("step %d dir %q, want %q", i, sp.Dir, want)
		}
	}
	data, next, err := h.svc.StepLog(context.Background(), id, 1, 0)
	if err != nil || string(data) != "hello\n" || next != 6 {
		t.Errorf("log %q %d %v", data, next, err)
	}
	if len(h.audit("build:"+string(id))) < 5 {
		t.Errorf("audit: %v", h.audit("build:"+string(id)))
	}
	h.status.mu.Lock()
	defer h.status.mu.Unlock()
	if len(h.status.calls) != 1 || len(h.status.calls[0]) != 2 {
		t.Errorf("status refresh calls %v", h.status.calls)
	}
}

func (h *harness) audit(subject string) []string {
	var out []string
	for _, e := range h.store.Audit() {
		if e.Subject == subject {
			out = append(out, e.Action)
		}
	}
	return out
}

func TestStopsAtFirstFailure(t *testing.T) {
	h := newHarness(t)
	h.start()
	id := h.enqueue(steps(exit(0), exit(3), exit(0)))
	f := h.finished(id)
	if r, ok := f.Result.(corebuilds.ResultFailed); !ok || r.Step != 1 {
		t.Fatalf("result %#v", f.Result)
	}
	if _, ok := f.Steps[2].(corebuilds.StepSkipped); !ok {
		t.Errorf("step 2 %#v", f.Steps[2])
	}
	if n := len(h.fake.Started()); n != 2 {
		t.Errorf("started %d", n)
	}
	// Failed builds have no artifacts to collect.
	if f.Artifacts != nil {
		t.Errorf("artifacts %v", f.Artifacts)
	}
}

func TestCancelRunningAndQueued(t *testing.T) {
	h := newHarness(t)
	h.start()
	first := h.enqueue(steps([]string{"fake", "block"}))
	second := h.enqueue(steps(exit(0)))
	h.wait(first, isRunning)
	h.wait(second, isQueued)
	if err := h.svc.Cancel(context.Background(), second, "user"); err != nil {
		t.Fatal(err)
	}
	f2 := h.finished(second)
	if _, ok := f2.Result.(corebuilds.ResultCancelled); !ok || f2.StartedAt != nil {
		t.Errorf("queued cancel: %#v", f2)
	}
	if err := h.svc.Cancel(context.Background(), first, "user"); err != nil {
		t.Fatal(err)
	}
	f1 := h.finished(first)
	if _, ok := f1.Result.(corebuilds.ResultCancelled); !ok {
		t.Fatalf("running cancel: %#v", f1.Result)
	}
	if d := f1.Steps[0].(corebuilds.StepDone); d.Outcome != (corebuilds.Cancelled{}) {
		t.Errorf("outcome %#v", d.Outcome)
	}
	if err := h.svc.Cancel(context.Background(), first, "user"); err != nil {
		t.Errorf("second cancel should be a no-op: %v", err)
	}
	if n := len(h.fake.Started()); n != 1 {
		t.Errorf("started %d, want only the first build", n)
	}
	if err := h.svc.Cancel(context.Background(), "bld_missing", "user"); err == nil {
		t.Error("cancel of an unknown build should fail")
	}
}

func TestStartFailureClasses(t *testing.T) {
	cases := []struct {
		name   string
		script platformtest.Script
		mod    func(*builds.Deps)
		want   corebuilds.StartFailure
	}{
		{"not found", platformtest.Script{StartErr: fmt.Errorf("x: %w", platform.ErrNotFound)}, nil, corebuilds.StartNotFound},
		{"bad args", platformtest.Script{StartErr: fmt.Errorf("x: %w", platform.ErrBatchArg)}, nil, corebuilds.StartBadArgs},
		{"dir missing", platformtest.Script{StartErr: fmt.Errorf("x: %w", platform.ErrDirMissing)}, nil, corebuilds.StartDirMissing},
		{"dir missing precheck", platformtest.Script{}, func(d *builds.Deps) { d.DirExists = func(string) bool { return false } }, corebuilds.StartDirMissing},
		{"other", platformtest.Script{StartErr: errors.New("boom")}, nil, corebuilds.StartOther},
		{"resolve not found", platformtest.Script{}, func(d *builds.Deps) {
			d.Resolve = func(string, string, []string) (string, error) { return "", platform.ErrNotFound }
		}, corebuilds.StartNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mods []func(*builds.Deps)
			if c.mod != nil {
				mods = append(mods, c.mod)
			}
			h := newHarness(t, mods...)
			h.fake.Push(c.script)
			h.start()
			id := h.enqueue(steps(exit(0), exit(0)))
			f := h.finished(id)
			if r, ok := f.Result.(corebuilds.ResultFailed); !ok || r.Step != 0 {
				t.Fatalf("result %#v", f.Result)
			}
			d := f.Steps[0].(corebuilds.StepDone)
			if sf, ok := d.Outcome.(corebuilds.StartFailed); !ok || sf.Reason != c.want {
				t.Errorf("outcome %#v, want %s", d.Outcome, c.want)
			}
			// A started step always has a log, even when it never ran (S5).
			data, _, err := h.svc.StepLog(context.Background(), id, 0, 0)
			if err != nil || len(data) == 0 {
				t.Errorf("start failure log %q %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(h.logs, "logs", "builds", string(id), "0-1.log")); err != nil {
				t.Errorf("log file: %v", err)
			}
		})
	}
}

func TestDuplicateStepExitedIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.start()
	id := h.enqueue(steps([]string{"fake", "block"}, exit(0)))
	h.wait(id, isRunning)
	// A stale result for a step that is not the running one changes nothing.
	h.svc.Post(id, corebuilds.StepExited{Index: 1, Attempt: 1, ExitCode: 0, Now: time.Now()})
	h.svc.Post(id, corebuilds.StepExited{Index: 0, Attempt: 2, ExitCode: 0, Now: time.Now()})
	time.Sleep(50 * time.Millisecond)
	st, _ := h.svc.Get(context.Background(), id)
	if r, ok := st.(corebuilds.Running); !ok || r.Current != 0 {
		t.Fatalf("state %#v", st)
	}
	if n := len(h.fake.Started()); n != 1 {
		t.Errorf("started %d", n)
	}
	// The real exit, delivered twice, advances once.
	h.svc.Post(id, corebuilds.StepExited{Index: 0, Attempt: 1, ExitCode: 0, Now: time.Now()})
	h.svc.Post(id, corebuilds.StepExited{Index: 0, Attempt: 1, ExitCode: 0, Now: time.Now()})
	f := h.finished(id)
	if _, ok := f.Result.(corebuilds.ResultSucceeded); !ok {
		t.Errorf("result %#v", f.Result)
	}
	if n := len(h.fake.Started()); n != 2 {
		t.Errorf("started %d, want 2 (one per step)", n)
	}
}

func TestRestartMarksRunningBuildInterruptedAndRunsQueued(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	spec := steps(exit(0), exit(0))
	run := corebuilds.Running{ID: "bld_old", Spec: spec, QueuedAt: t0, StartedAt: t0, Actor: "user",
		Steps: []corebuilds.StepState{corebuilds.StepRunning{Attempt: 1, StartedAt: t0}, corebuilds.StepPending{}}}
	queued := corebuilds.Queued{ID: "bld_wait", Spec: steps(exit(0)), QueuedAt: t0.Add(time.Second), Actor: "user"}
	err := h.store.Commit(context.Background(), func(tx builds.BuildTx) error {
		if _, err := tx.SaveBuild(context.Background(), run, builds.NewBuild); err != nil {
			return err
		}
		_, err := tx.SaveBuild(context.Background(), queued, builds.NewBuild)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	f := h.finished("bld_old")
	if r, ok := f.Result.(corebuilds.ResultInterrupted); !ok || r.Step != 0 {
		t.Fatalf("result %#v", f.Result)
	}
	if d := f.Steps[0].(corebuilds.StepDone); d.Outcome != (corebuilds.Interrupted{}) {
		t.Errorf("outcome %#v", d.Outcome)
	}
	if _, ok := h.finished("bld_wait").Result.(corebuilds.ResultSucceeded); !ok {
		t.Error("the queued build should run after the restart")
	}
	if n := len(h.fake.Started()); n != 1 {
		t.Errorf("the interrupted build must not restart: %d processes", n)
	}
}

func TestQueueAdmitsOneAtATime(t *testing.T) {
	h := newHarness(t)
	h.start()
	a := h.enqueue(steps([]string{"fake", "block"}))
	b := h.enqueue(steps([]string{"fake", "block"}))
	c := h.enqueue(steps(exit(0)))
	h.wait(a, isRunning)
	for i := 0; i < 10; i++ {
		n, _ := h.store.CountRunning(context.Background())
		if n != 1 {
			t.Fatalf("running = %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.wait(b, isQueued)
	h.wait(c, isQueued)
	busy, err := h.svc.BusyMainCheckouts(context.Background(), []core.RepoID{"rep_a", "rep_b"})
	if err != nil || len(busy) != 1 || !strings.Contains(busy["rep_a"], "running") {
		t.Errorf("busy %v %v", busy, err)
	}
	_ = h.svc.Cancel(context.Background(), a, "user")
	h.wait(b, isRunning)
	n, _ := h.store.CountRunning(context.Background())
	if n != 1 {
		t.Errorf("running = %d", n)
	}
	_ = h.svc.Cancel(context.Background(), b, "user")
	h.finished(c)
	if len(h.fake.Started()) != 3 {
		t.Errorf("started %d", len(h.fake.Started()))
	}
}

func TestLogRedactsTokensInFileAndStream(t *testing.T) {
	h := newHarness(t)
	h.fake.Push(platformtest.Script{Stdout: "using token " + sentinel + "\nplain line\n", Stderr: "err " + sentinel})
	h.start()
	id := h.enqueue(steps(exit(0)))
	h.finished(id)
	data, _, err := h.svc.StepLog(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) || !strings.Contains(string(data), "plain line") {
		t.Errorf("log file: %q", data)
	}
	raw, err := os.ReadFile(filepath.Join(h.logs, "logs", "builds", string(id), "0-1.log"))
	if err != nil || strings.Contains(string(raw), sentinel) {
		t.Errorf("raw log %q %v", raw, err)
	}
	h.pub.mu.Lock()
	defer h.pub.mu.Unlock()
	if len(h.pub.lines) != 3 {
		t.Errorf("stream lines %v", h.pub.lines)
	}
	for _, l := range h.pub.lines {
		if strings.Contains(l, sentinel) {
			t.Errorf("stream leaks the token: %q", l)
		}
	}
	for _, c := range h.pub.changed {
		if strings.Contains(c, sentinel) {
			t.Error("changed message leaks")
		}
	}
	for _, e := range h.store.Audit() {
		if strings.Contains(string(e.Detail), sentinel) {
			t.Error("audit leaks")
		}
	}
}

func TestLogTruncatedIsMarked(t *testing.T) {
	h := newHarness(t)
	h.start()
	id := h.enqueue(steps([]string{"fake", "leak", "0"}))
	f := h.finished(id)
	if d := f.Steps[0].(corebuilds.StepDone); !d.LogTruncated {
		t.Errorf("step %#v", d)
	}
}

func TestWorktreeBuildRunsInTheWorktree(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt dir")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(d *builds.Deps) {
		d.Locator = locator{repos: map[core.RepoID]string{"rep_a": "/does/not/exist"}, worktrees: map[core.WorktreeID]string{"wt_1": wt}}
	})
	h.start()
	spec := steps(exit(0))
	spec.Target = corebuilds.InWorktree{WorktreeID: "wt_1"}
	id := h.enqueue(spec)
	h.finished(id)
	if got := h.fake.Started(); len(got) != 1 || got[0].Dir != wt {
		t.Errorf("started %+v", got)
	}
}

func TestBuildStartedSubscription(t *testing.T) {
	h := newHarness(t)
	ch := make(chan builds.BuildStarted, 4)
	h.svc.SubscribeStarted(func(b builds.BuildStarted) { ch <- b })
	h.start()
	id := h.enqueue(steps(exit(0), exit(0)))
	select {
	case ev := <-ch:
		if ev.ID != id || len(ev.Repos) != 2 {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no BuildStarted")
	}
}

func TestPresetsCRUDAndRun(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctx := context.Background()
	p, err := h.svc.CreatePreset(ctx, []byte(`{"projectId":"prj_1","name":"verify","steps":[{"repoId":"rep_a","name":"t","argv":["fake","exit","0"]}]}`), "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreatePreset(ctx, []byte(`{"projectId":"prj_1","name":"verify","steps":[{"repoId":"rep_a","name":"t","argv":["fake","exit","0"]}]}`), "user"); err == nil {
		t.Error("duplicate name accepted")
	}
	p2, err := h.svc.UpdatePreset(ctx, p.ID, []byte(`{"name":"verify all"}`), "user")
	if err != nil || p2.Name != "verify all" || len(p2.Steps) != 1 {
		t.Fatalf("update %+v %v", p2, err)
	}
	if _, err := h.svc.UpdatePreset(ctx, p.ID, []byte(`{"projectId":"prj_2"}`), "user"); err == nil {
		t.Error("changing the project accepted")
	}
	list, err := h.svc.ListPresets(ctx, "prj_1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	id, err := h.svc.RunPreset(ctx, p.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	f := h.finished(id)
	if f.Spec.PresetID != p.ID {
		t.Errorf("preset id %q", f.Spec.PresetID)
	}
	if got := h.audit("preset:" + string(p.ID)); len(got) != 2 {
		t.Errorf("preset audit %v", got)
	}
}

func TestEnqueueRequestRejectsUnknownRepo(t *testing.T) {
	h := newHarness(t)
	h.start()
	_, err := h.svc.EnqueueRequest(context.Background(), []byte(`{"target":"main","steps":[{"repoId":"rep_zzz","name":"x","argv":["a"]}]}`), "user")
	if err == nil {
		t.Fatal("unknown repo accepted")
	}
	id, err := h.svc.EnqueueRequest(context.Background(), []byte(`{"target":"main","steps":[{"repoId":"rep_a","name":"x","argv":["fake","exit","0"]}]}`), "user")
	if err != nil {
		t.Fatal(err)
	}
	h.finished(id)
}

func TestCrashPointsStartAtMostOnce(t *testing.T) {
	// S1: restarting the service on the same store never starts a step again.
	h := newHarness(t)
	h.start()
	id := h.enqueue(steps([]string{"fake", "block"}))
	h.wait(id, isRunning)
	h.shutdown()
	h2 := newHarness(t)
	h2.store = h.store
	svc, err := builds.NewService(builds.Deps{
		Store: h.store, Start: h2.fake.Start, Locator: locator{repos: h.dirs}, Env: safety.NewEnvBuilder(), DataDir: h2.logs,
		Resolve: func(name, _ string, _ []string) (string, error) { return name, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	h2.svc = svc
	h2.start()
	f := h2.finished(id)
	if _, ok := f.Result.(corebuilds.ResultInterrupted); !ok {
		t.Errorf("result %#v", f.Result)
	}
	if n := len(h2.fake.Started()); n != 0 {
		t.Errorf("restart started %d processes", n)
	}
}
