package devservers_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// M1: a full port range is "no block": the server starts without port
// variables and the view says so; a command that needs {port} is refused.
func TestFullPortRangeStartsWithoutInjectionAndWarns(t *testing.T) {
	r := newRig(t)
	r.blocks.Err = fmt.Errorf("allocate block for wt_1: %w", worktreeenv.ErrRangeFull)
	v, err := r.svc.Start(context.Background(), r.target, "dev")
	if err != nil {
		t.Fatalf("a full range refused the start: %v", err)
	}
	if v.Warning == "" || !v.Block.Empty() {
		t.Errorf("view = %+v", v)
	}
	eventually(t, "process", func() bool { return len(r.fake.Started()) == 1 })
	if env := strings.Join(r.fake.Started()[0].Env, "\n"); strings.Contains(env, "PORT=") || strings.Contains(env, "YGG_PORT") {
		t.Errorf("port variables injected without a block: %s", env)
	}
	if got := r.get(); got.Warning == "" {
		t.Error("the warning is gone from the next view")
	}
	// A second start is refused and must not erase the running server's warning.
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err == nil {
		t.Fatal("second start accepted")
	}
	if r.get().Warning == "" {
		t.Error("a refused start erased the warning")
	}
}

func TestFullPortRangeRefusesATemplateThatNeedsAPort(t *testing.T) {
	r := newRig(t)
	r.blocks.Err = worktreeenv.ErrRangeFull
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"vite", "--port", "{port}"}})
	_, err := r.svc.Start(context.Background(), r.target, "dev")
	if c, _ := apperr.ClassOf(err); c != apperr.Invalid {
		t.Fatalf("err = %v", err)
	}
	if len(r.fake.Started()) != 0 {
		t.Error("a process started")
	}
	// Any other block error still refuses the start.
	r.blocks.Err = errors.New("database is locked")
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "block"}})
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err == nil {
		t.Error("a block error other than a full range started the server")
	}
}

// M2: a restart while the first attempt has no pid yet must not let the old
// attempt's kill or forget reach the new attempt.
func TestRestartDuringStartingDoesNotTouchTheNewAttempt(t *testing.T) {
	r := newRig(t)
	gate := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	r.resolve = func(name, _ string, _ []string) (string, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			<-gate // the first attempt is still preparing: Starting without a pid
		}
		return name, nil
	}
	first, err := r.svc.Start(context.Background(), r.target, "dev")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first attempt preparing", func() bool { mu.Lock(); defer mu.Unlock(); return calls == 1 })
	if v := r.get(); v.Status != "starting" || v.Pid != 0 {
		t.Fatalf("view = %+v", v)
	}
	if _, err := r.svc.Restart(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	old := devservers.ProcessID(first.ID, 1)
	eventually(t, "old attempt killed", func() bool { return slices.Contains(r.fake.KilledTasks(), old) })
	close(gate)
	newID := devservers.ProcessID(first.ID, 2)
	eventually(t, "new attempt running", func() bool {
		for _, sp := range r.fake.Started() {
			if sp.TaskID == newID {
				return r.get().Pid != 0
			}
		}
		return false
	})
	time.Sleep(50 * time.Millisecond) // let the old goroutine finish
	if v := r.get(); v.Attempt != 2 || v.Status != "starting" && v.Status != "running" {
		t.Errorf("the new attempt did not survive: %+v", v)
	}
	if slices.Contains(r.fake.KilledTasks(), newID) {
		t.Error("the old attempt's kill reached the new attempt")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.forgot, newID) {
		t.Error("the old attempt erased the new attempt's orphan record")
	}
	if !slices.Contains(r.recorded, newID) {
		t.Errorf("new attempt not recorded: %v", r.recorded)
	}
}

// S2: an exit whose commit fails is retried, not lost.
func TestExitIsRetriedWhenTheCommitFails(t *testing.T) {
	r := newRig(t)
	v := r.toRunning(20001)
	r.store.FailCommit = errors.New("disk is busy")
	_ = r.fake.KillTask(devservers.ProcessID(v.ID, v.Attempt))
	got := r.waitStatus("failed")
	if got.Fail != dsc.FailCrashed {
		t.Errorf("view = %+v", got)
	}
}

// Safety: a failed orphan stop is audited and published; the orphan stays.
func TestFailedOrphanStopIsAuditedAndPublished(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	k := dsc.Key{Repo: "rep_1", CommandKey: "old"}
	id := devservers.IDOf(k)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := r.store.Commit(ctx, func(tx devservers.Tx) error {
		_, err := tx.Save(ctx, id, dsc.RowOf(k, dsc.Running{Attempt: 1, Since: t0, Pid: 12}), db.NewDevServer, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pid := r.orph.Spawn()
	_ = r.orph.Orphans().Record(devservers.ProcessID(id, 1), pid)
	if err := r.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r.stopErr = errors.New("access is denied")
	target := devservers.Target{Repo: "rep_1"}
	r.repos.Track(target, r.dir)
	if _, err := r.svc.Stop(ctx, target, "old"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "failure audited", func() bool { return slices.Contains(r.actions(), "devserver.orphan_stop_failed") })
	row, _ := r.store.Get(ctx, id)
	if row.Row.Status != "orphaned" || !r.orph.Alive(pid) {
		t.Errorf("status %q, alive %v", row.Row.Status, r.orph.Alive(pid))
	}
	published := 0
	for _, s := range r.hub.Statuses() {
		if s == "orphaned" {
			published++
		}
	}
	if published < 2 { // once at recovery, once for the failed stop
		t.Errorf("published orphaned %d times, want 2", published)
	}
	// The user tries again once the cause is gone.
	r.stopErr = nil
	if _, err := r.svc.Stop(ctx, target, "old"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "orphan stopped", func() bool { row, _ := r.store.Get(ctx, id); return row.Row.Status == "stopped" })
}

// S3: one server's slow hub does not hold back another server.
type slowHub struct {
	devserverstest.Hub
	hold    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (h *slowHub) PublishServer(id string, v devservers.View) {
	if v.Key.CommandKey == "slow" {
		h.once.Do(func() { close(h.entered) })
		<-h.hold
	}
	h.Hub.PublishServer(id, v)
}

func TestSlowPublishDoesNotBlockOtherServers(t *testing.T) {
	r := newRig(t)
	hub := &slowHub{hold: make(chan struct{}), entered: make(chan struct{})}
	r.svc.Close(context.Background())
	r.hub = &hub.Hub
	// Rebuild the service with the slow hub.
	o := r.orph.Orphans()
	svc, err := devservers.New(context.Background(), devservers.Deps{
		Store: r.store, Repos: r.repos, Trust: r.trust, Templates: r.tpls, Blocks: r.blocks,
		Start: r.fake.Start, KillTask: r.fake.KillTask,
		Resolve: func(n, _ string, _ []string) (string, error) { return n, nil },
		Ports:   r.ports.get, Orphans: devservers.Orphans{Record: o.Record, Forget: o.Forget, List: o.List, Stop: o.Stop},
		Env: safety.NewEnvBuilder(), Redactor: safety.NewRedactor(), Hub: hub, Clock: r.clock, IDs: db.NewID, DataDir: r.dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(hub.hold); svc.Close(context.Background()) })
	r.tpls.Set("slow", devserverstest.Command{Argv: []string{"fake", "block"}})
	go func() { _, _ = svc.Start(context.Background(), r.target, "slow") }()
	select {
	case <-hub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow publish never started")
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Start(context.Background(), r.target, "dev"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a slow hub for one server blocked another")
	}
}

var _ = core.PortBlock{}
