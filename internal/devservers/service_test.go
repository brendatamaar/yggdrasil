package devservers_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

type portsSrc struct {
	mu    sync.Mutex
	ports []int
	err   error
	calls int
}

func (p *portsSrc) set(ports ...int) { p.mu.Lock(); p.ports = ports; p.mu.Unlock() }

func (p *portsSrc) get(int) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return slices.Clone(p.ports), p.err
}

type rig struct {
	t       *testing.T
	svc     *devservers.Service
	store   *devserverstest.Store
	clock   *devserverstest.FakeClock
	fake    *platformtest.Fake
	repos   *devserverstest.Repos
	trust   *devserverstest.Trust
	tpls    *devserverstest.Templates
	blocks  *devserverstest.Blocks
	hub     *devserverstest.Hub
	orph    *platformtest.FakeOrphans
	ports   *portsSrc
	target  devservers.Target
	dir     string
	dataDir string

	mu       sync.Mutex
	recorded []string
	forgot   []string
	resolve  func(name, dir string, env []string) (string, error)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t: t, store: devserverstest.NewStore("rep_1"), clock: devserverstest.NewClock(), fake: &platformtest.Fake{},
		repos: devserverstest.NewRepos(), trust: &devserverstest.Trust{}, tpls: &devserverstest.Templates{},
		blocks: &devserverstest.Blocks{}, hub: &devserverstest.Hub{}, orph: platformtest.NewFakeOrphans(), ports: &portsSrc{},
		target: devservers.Target{Repo: "rep_1", Worktree: "wt_1"}, dir: filepath.Join(t.TempDir(), "my worktree"), dataDir: t.TempDir(),
		resolve: func(name, _ string, _ []string) (string, error) { return name, nil },
	}
	r.repos.Track(r.target, r.dir)
	r.repos.Track(devservers.Target{Repo: "rep_1"}, r.dir)
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "block"}})
	r.build(r.store)
	return r
}

func (r *rig) build(store devservers.Store) {
	o := r.orph.Orphans()
	svc, err := devservers.New(context.Background(), devservers.Deps{
		Store: store, Repos: r.repos, Trust: r.trust, Templates: r.tpls, Blocks: r.blocks,
		Start: r.fake.Start, KillTask: r.fake.KillTask,
		Resolve: func(n, d string, e []string) (string, error) { return r.resolve(n, d, e) },
		Ports:   r.ports.get,
		Orphans: devservers.Orphans{
			Record: func(id string, pid int) error {
				r.mu.Lock()
				r.recorded = append(r.recorded, id)
				r.mu.Unlock()
				return o.Record(id, pid)
			},
			Forget: func(id string) error {
				r.mu.Lock()
				r.forgot = append(r.forgot, id)
				r.mu.Unlock()
				return o.Forget(id)
			},
			List: o.List, Stop: o.Stop,
		},
		Env: safety.NewEnvBuilder(), Redactor: safety.NewRedactor(), Hub: r.hub, Clock: r.clock,
		IDs: db.NewID, DataDir: r.dataDir,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.svc = svc
	r.t.Cleanup(func() { svc.Close(context.Background()) })
}

func (r *rig) get() devservers.View {
	r.t.Helper()
	v, err := r.svc.Get(context.Background(), r.target, "dev")
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *rig) waitStatus(want string) devservers.View {
	r.t.Helper()
	var v devservers.View
	eventually(r.t, "status "+want, func() bool { v = r.get(); return v.Status == want })
	return v
}

// toRunning starts the server and walks it to Running on the given ports.
func (r *rig) toRunning(ports ...int) devservers.View {
	r.t.Helper()
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		r.t.Fatal(err)
	}
	eventually(r.t, "pid", func() bool { return r.get().Pid != 0 })
	r.ports.set(ports...)
	r.clock.Advance(time.Second)
	return r.waitStatus("running")
}

func (r *rig) actions() []string {
	var out []string
	for _, e := range r.store.Audit("") {
		out = append(out, e.Action)
	}
	return out
}

func TestStartRunsInTheTrackedFolderWithTheCleanEnvAndPortBlock(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	r := newRig(t)
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "block"}, Env: map[string]string{"NODE_ENV": "development"}})
	v := r.toRunning(20003)
	if v.Block != (core.PortBlock{Base: 20000, Size: 20}) || !slices.Equal(v.Ports, []dsc.Port{20003}) || len(v.Warnings) != 0 {
		t.Fatalf("view = %+v", v)
	}
	specs := r.fake.Started()
	if len(specs) != 1 {
		t.Fatalf("started %d processes", len(specs))
	}
	spec := specs[0]
	if spec.Dir != r.dir || spec.TaskID != v.ID {
		t.Errorf("dir/task = %q / %q", spec.Dir, spec.TaskID)
	}
	env := strings.Join(spec.Env, "\n")
	for _, want := range []string{"PORT=20000", "YGG_PORT_BASE=20000", "YGG_PORT_0=20000", "YGG_PORT_19=20019", "NODE_ENV=development"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "ghp_") || strings.Contains(env, "GITHUB_TOKEN") {
		t.Error("a token reached the child process")
	}
	if got := r.actions(); !slices.Equal(got, []string{"devserver.start", "devserver.running"}) {
		t.Errorf("audit = %v", got)
	}
	for _, e := range r.store.Audit("") {
		if e.Subject != "devserver:"+v.ID || e.Actor == "" || strings.Contains(string(e.Detail), r.dir) {
			t.Errorf("audit entry %+v", e)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.recorded, []string{v.ID}) {
		t.Errorf("recorded = %v", r.recorded)
	}
}

func TestPlaceholdersAreExpandedInsideTheBlock(t *testing.T) {
	r := newRig(t)
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "print", "Local: http://localhost:{port:2}"}})
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("failed") // the fake script exits at once
	lines, _ := r.svc.Logs(r.target, "dev", -1)
	if !slices.Contains(lines, "Local: http://localhost:20002") {
		t.Errorf("log = %v", lines)
	}
}

func TestMainCheckoutHasNoBlockAndNoPortEnv(t *testing.T) {
	r := newRig(t)
	r.target = devservers.Target{Repo: "rep_1"}
	r.toRunning(3000)
	if env := strings.Join(r.fake.Started()[0].Env, "\n"); strings.Contains(env, "PORT=") {
		t.Errorf("port env injected into a main checkout: %s", env)
	}
	if v := r.get(); len(v.Warnings) != 0 {
		t.Errorf("warnings without a block: %v", v.Warnings)
	}
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "print", "{port}"}})
	if _, err := r.svc.Stop(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("stopped")
	_, err := r.svc.Start(context.Background(), r.target, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.Invalid {
		t.Errorf("placeholder without a block: %v", err)
	}
}

func TestPortOutsideTheBlockWarnsAndNeverKills(t *testing.T) {
	r := newRig(t)
	v := r.toRunning(3000, 20001)
	if !slices.Equal(v.Warnings, []dsc.Port{3000}) {
		t.Fatalf("warnings = %v", v.Warnings)
	}
	if got := r.fake.KilledTasks(); len(got) != 0 {
		t.Errorf("killed %v", got)
	}
}

func TestAnnouncedPortsAreBatchedIntoTheState(t *testing.T) {
	r := newRig(t)
	r.fake.Push(platformtest.Script{Stdout: "ready\nLocal: http://localhost:20005/\nalso 127.0.0.1:20006\n", Block: true})
	v := r.toRunning(20005)
	r.clock.Advance(300 * time.Millisecond)
	eventually(t, "announced", func() bool { return slices.Equal(r.get().Announced, []dsc.Port{20005, 20006}) })
	_ = v
}

func TestPollingKeepsPortsAndSurvivesErrors(t *testing.T) {
	r := newRig(t)
	r.toRunning(20001)
	r.ports.mu.Lock()
	r.ports.err = errors.New("access denied")
	r.ports.mu.Unlock()
	r.clock.Advance(2 * time.Second)
	eventually(t, "a poll", func() bool { r.ports.mu.Lock(); defer r.ports.mu.Unlock(); return r.ports.calls >= 2 })
	if v := r.get(); v.Status != "running" || !slices.Equal(v.Ports, []dsc.Port{20001}) {
		t.Errorf("after a failed poll: %+v", v)
	}
	r.ports.mu.Lock()
	r.ports.err = nil
	r.ports.ports = []int{20001, 20002, 0, 70000}
	r.ports.mu.Unlock()
	r.clock.Advance(2 * time.Second)
	eventually(t, "new ports", func() bool { return slices.Equal(r.get().Ports, []dsc.Port{20001, 20002}) })
}

func TestSettleMakesAServerRunningWithoutPorts(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pid", func() bool { return r.get().Pid != 0 })
	r.clock.Advance(dsc.SettleAfter)
	if v := r.waitStatus("running"); len(v.Ports) != 0 {
		t.Errorf("ports = %v", v.Ports)
	}
}

func TestStopKillsAndRecordsUserStop(t *testing.T) {
	r := newRig(t)
	r.toRunning(20001)
	v, err := r.svc.Stop(context.Background(), r.target, "dev")
	if err != nil || v.Status != "stopping" {
		t.Fatalf("Stop = %+v, %v", v, err)
	}
	got := r.waitStatus("stopped")
	if got.Last != dsc.LastUserStopped {
		t.Errorf("last = %q", got.Last)
	}
	if !slices.Contains(r.fake.KilledTasks(), v.ID) {
		t.Errorf("job not killed: %v", r.fake.KilledTasks())
	}
	want := []string{"devserver.start", "devserver.running", "devserver.stop", "devserver.exit"}
	if got := r.actions(); !slices.Equal(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.forgot, v.ID) {
		t.Error("the process was not forgotten after it exited")
	}
}

func TestEarlyExitFailsAndIsNeverRestarted(t *testing.T) {
	r := newRig(t)
	r.fake.Push(platformtest.Script{Stdout: "Error: port in use\n", Exit: 1})
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	v := r.waitStatus("failed")
	if v.Fail != dsc.FailExitedEarly || v.ExitCode != 1 || v.Message == "" {
		t.Errorf("view = %+v", v)
	}
	r.clock.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if n := len(r.fake.Started()); n != 1 {
		t.Errorf("started %d times, want 1 (no automatic restart)", n)
	}
	lines, _ := r.svc.Logs(r.target, "dev", 50)
	if !slices.Contains(lines, "Error: port in use") {
		t.Errorf("log = %v", lines)
	}
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Errorf("start after a failure: %v", err)
	}
}

func TestCrashOfARunningServer(t *testing.T) {
	r := newRig(t)
	r.fake.Push(platformtest.Script{Block: true})
	v := r.toRunning(20001)
	_ = r.fake.KillTask(v.ID) // the process dies by itself, exit code 1
	got := r.waitStatus("failed")
	if got.Fail != dsc.FailCrashed {
		t.Errorf("fail = %q", got.Fail)
	}
}

func TestSecondStartIsRefused(t *testing.T) {
	r := newRig(t)
	r.toRunning(20001)
	_, err := r.svc.Start(context.Background(), r.target, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.Conflict {
		t.Fatalf("second start = %v", err)
	}
	if n := len(r.fake.Started()); n != 1 {
		t.Errorf("started %d processes", n)
	}
}

func TestUntrackedAndUntrustedTargetsNeverStart(t *testing.T) {
	r := newRig(t)
	other := devservers.Target{Repo: "rep_1", Worktree: "wt_gone"}
	_, err := r.svc.Start(context.Background(), other, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Errorf("untracked = %v", err)
	}
	r.trust.Set("rep_1", guard.TrustUntrusted)
	_, err = r.svc.Start(context.Background(), r.target, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.Conflict || !strings.Contains(err.Error(), "trust") {
		t.Errorf("untrusted = %v", err)
	}
	r.trust.Err = errors.New("scan failed")
	r.trust.Set("rep_1", guard.TrustTrusted)
	if _, err = r.svc.Start(context.Background(), r.target, "dev"); err == nil {
		t.Error("an unreadable trust view started a server")
	}
	if n := len(r.fake.Started()) + r.store.Commits(); n != 0 {
		t.Errorf("something ran or was written (%d)", n)
	}
}

func TestParseStartRejectsPathLikeInput(t *testing.T) {
	for _, tc := range [][3]string{
		{"", "", "dev"}, {"rep 1", "", "dev"}, {"../x", "", "dev"}, {"rep_1", "a/b", "dev"}, {"rep_1", `a\b`, "dev"},
		{"rep_1", "", ""}, {"rep_1", "", "../dev"}, {"rep_1", "", "a b"}, {"rep_1", "", ".hidden"}, {"rep_1", "", strings.Repeat("a", 65)},
	} {
		if _, err := devservers.ParseStart(tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("ParseStart(%q) accepted", tc[:])
		}
	}
	k, err := devservers.ParseStart("rep_1", "wt_1", "dev-2.x")
	if err != nil || k.CommandKey != "dev-2.x" {
		t.Errorf("valid input: %+v %v", k, err)
	}
	if id := devservers.IDOf(k); platform.CheckProcessID(id) != nil || id != devservers.IDOf(k) {
		t.Errorf("id %q is not a valid process id", id)
	}
}

func TestMissingProgramIsAPlainFailure(t *testing.T) {
	r := newRig(t)
	r.resolve = func(string, string, []string) (string, error) { return "", platform.ErrNotFound }
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"dotnet", "run"}})
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	v := r.waitStatus("failed")
	if v.Fail != dsc.FailStartFailed || !strings.Contains(v.Message, "Install dotnet") {
		t.Errorf("view = %+v", v)
	}
	if len(r.fake.Started()) != 0 {
		t.Error("a process started")
	}
}

func TestPanicInTheInterpreterIsRecovered(t *testing.T) {
	r := newRig(t)
	r.resolve = func(string, string, []string) (string, error) { panic("boom") }
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("failed")
	r.resolve = func(n, _ string, _ []string) (string, error) { return n, nil }
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Errorf("the service is unusable after a panic: %v", err)
	}
}

func TestLogsAreRedactedInRingFileAndHub(t *testing.T) {
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	r := newRig(t)
	r.fake.Push(platformtest.Script{Stdout: "using token " + tok + "\n", Exit: 0})
	v, err := r.svc.Start(context.Background(), r.target, "dev")
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus("failed")
	lines, _ := r.svc.Logs(r.target, "dev", -1)
	all := strings.Join(lines, "\n")
	for _, l := range r.hub.LinesOf(v.ID) {
		all += "\n" + l
	}
	data, err := os.ReadFile(filepath.Join(r.dataDir, "logs", "devservers", v.ID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	all += string(data)
	if strings.Contains(all, tok) || !strings.Contains(all, "[REDACTED]") {
		t.Errorf("log leaks or lacks redaction:\n%s", all)
	}
}

func TestRestartStopsThenStartsANewAttempt(t *testing.T) {
	r := newRig(t)
	first := r.toRunning(20001)
	if _, err := r.svc.Restart(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second process", func() bool { return len(r.fake.Started()) == 2 })
	if v := r.get(); v.Attempt != first.Attempt+1 {
		t.Errorf("attempt = %d, want %d", v.Attempt, first.Attempt+1)
	}
}

func TestCommitFailureStartsNothing(t *testing.T) {
	r := newRig(t)
	r.store.FailCommit = errors.New("disk full")
	_, err := r.svc.Start(context.Background(), r.target, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.Unavailable {
		t.Fatalf("err = %v", err)
	}
	if len(r.fake.Started()) != 0 || len(r.hub.Statuses()) != 0 {
		t.Error("a process started or a status was published although nothing was saved")
	}
}

func TestVersionConflictIsRetried(t *testing.T) {
	r := newRig(t)
	r.store.FailCommit = db.ErrConflict
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatalf("start after one conflict: %v", err)
	}
}

func TestCloseDrainsEveryServerBeforeReturning(t *testing.T) {
	r := newRig(t)
	v := r.toRunning(20001)
	r.svc.Close(context.Background())
	row, err := r.store.Get(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	st, err := dsc.ServerFromRow(row.Row)
	if s, ok := st.(dsc.Stopped); err != nil || !ok || s.Last != dsc.LastInterrupted {
		t.Fatalf("after Close: %#v %v", st, err)
	}
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err == nil {
		t.Error("a start was accepted after Close")
	}
	n := r.store.Commits()
	r.clock.Advance(time.Minute)
	if r.store.Commits() != n {
		t.Error("a timer committed after Close")
	}
}

func TestReconcileAfterACrashedRun(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	b := core.PortBlock{Base: 20000, Size: 20}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := map[string]dsc.Server{
		"gone":  dsc.Running{Attempt: 2, Since: t0, Pid: 11, Block: b, Ports: []dsc.Port{20000}, Announced: []dsc.Port{}},
		"alive": dsc.Running{Attempt: 1, Since: t0, Pid: 12, Block: b, Ports: []dsc.Port{}, Announced: []dsc.Port{}},
		"start": dsc.Starting{Attempt: 3, Since: t0},
		"stopg": dsc.Stopping{Attempt: 1, Cause: dsc.CauseUserStop, KillAttempt: 1, Since: t0, Pid: 13},
		"idle":  dsc.Stopped{},
		"old":   dsc.Orphaned{Attempt: 1, Pid: 14, SeenAt: t0},
	}
	keys := map[string]dsc.Key{}
	for name, st := range seed {
		k := dsc.Key{Repo: "rep_1", CommandKey: name}
		keys[name] = k
		if err := r.store.Commit(ctx, func(tx devservers.Tx) error {
			_, err := tx.Save(ctx, devservers.IDOf(k), dsc.RowOf(k, st), db.NewDevServer, t0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	pid := r.orph.Spawn()
	_ = r.orph.Orphans().Record(devservers.IDOf(keys["alive"]), pid)
	if err := r.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	status := func(name string) dsc.Server {
		row, err := r.store.Get(ctx, devservers.IDOf(keys[name]))
		if err != nil {
			t.Fatal(err)
		}
		st, err := dsc.ServerFromRow(row.Row)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	for _, name := range []string{"gone", "start", "stopg", "old"} {
		if s, ok := status(name).(dsc.Stopped); !ok || s.Last != dsc.LastInterrupted {
			t.Errorf("%s = %#v, want Stopped{Interrupted}", name, status(name))
		}
	}
	if o, ok := status("alive").(dsc.Orphaned); !ok || o.Pid != pid || o.Attempt != 1 {
		t.Errorf("alive = %#v", status("alive"))
	}
	if _, ok := status("idle").(dsc.Stopped); !ok {
		t.Error("a server at rest changed")
	}
	if live, _ := r.store.ListLive(ctx); len(live) != 1 {
		t.Errorf("%d live rows left, want 1 (the orphan)", len(live))
	}
	if len(r.store.Audit("")) < 5 {
		t.Errorf("audit entries = %d, want one per changed server", len(r.store.Audit("")))
	}

	// The user stops the orphan: StopOrphan runs and its end is audited.
	target := devservers.Target{Repo: "rep_1"}
	r.repos.Track(target, r.dir)
	if _, err := r.svc.Stop(ctx, target, "alive"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "orphan stopped", func() bool { _, ok := status("alive").(dsc.Stopped); return ok })
	if r.orph.Alive(pid) {
		t.Error("the orphan process is still alive")
	}
	if !slices.Contains(r.actions(), "devserver.orphaned") {
		t.Errorf("no orphan audit: %v", r.actions())
	}
}

func TestPublishFollowsEveryStatusChange(t *testing.T) {
	r := newRig(t)
	r.toRunning(20001)
	if _, err := r.svc.Stop(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	r.waitStatus("stopped")
	eventually(t, "published stopped", func() bool { return slices.Contains(r.hub.Statuses(), "stopped") })
	st := r.hub.Statuses()
	for _, want := range []string{"starting", "running", "stopping", "stopped"} {
		if !slices.Contains(st, want) {
			t.Errorf("statuses %v lack %s", st, want)
		}
	}
}

func TestEveryTargetHasItsOwnPortBlock(t *testing.T) {
	r := newRig(t)
	other := devservers.Target{Repo: "rep_1", Worktree: "wt_2"}
	r.repos.Track(other, r.dir)
	r.toRunning(20001)
	if _, err := r.svc.Start(context.Background(), other, "dev"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second process", func() bool { return len(r.fake.Started()) == 2 })
	a, b := r.fake.Started()[0].Env, r.fake.Started()[1].Env
	if !slices.Contains(a, "PORT=20000") || !slices.Contains(b, "PORT=20020") {
		t.Errorf("ports not distinct: %v / %v", a, b)
	}
}
