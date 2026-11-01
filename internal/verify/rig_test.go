package verify_test

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/verify"
	"github.com/brendatamaar/yggdrasil/internal/verify/verifytest"
)

const secret = "sentinel-secret-value-4711"

// fakeTC is a toolchain whose commands the test sets. It detects a folder
// that has a "marker" file and hashes "deps.lock".
type fakeTC struct {
	bootstrap, verify, fullTests []builds.Step
}

func (fakeTC) Name() string { return "faketc" }
func (fakeTC) Detect(repo fs.FS) (builds.Detection, bool) {
	if _, err := fs.Stat(repo, "marker"); err != nil {
		return builds.Detection{}, false
	}
	return builds.Detection{Toolchain: "faketc", Dir: "."}, true
}
func (t fakeTC) Commands(builds.Detection) builds.Commands {
	return builds.Commands{Bootstrap: t.bootstrap, Verify: t.verify, FullTests: t.fullTests}
}
func (fakeTC) Artifacts(builds.Detection) []string { return nil }
func (fakeTC) DepsFiles(builds.Detection) []string { return []string{"deps.lock"} }

func exit(code int) []string { return []string{"fake", "exit", fmt.Sprint(code)} }

func step(name string, code int) builds.Step { return builds.Step{Name: name, Argv: exit(code)} }

type rig struct {
	t      *testing.T
	store  *verifytest.Store
	runner *verifytest.Runner
	msg    *verifytest.Messenger
	probe  *verifytest.Probe
	hub    *verifytest.Hub
	tc     *fakeTC
	files  fstest.MapFS
	dir    string
	svc    *verify.Service
	n      atomic.Int64
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t: t, store: verifytest.NewStore(), runner: verifytest.NewRunner(), msg: &verifytest.Messenger{},
		probe: &verifytest.Probe{Changes: map[string]bool{}}, hub: &verifytest.Hub{},
		tc:    &fakeTC{bootstrap: []builds.Step{step("install", 0)}, verify: []builds.Step{step("lint", 0), step("test", 0)}},
		files: fstest.MapFS{"marker": {Data: []byte("x")}, "deps.lock": {Data: []byte("v1")}},
		dir:   filepath.Join(t.TempDir(), "wt 1"),
	}
	r.store.AddWorktree(core.Worktree{ID: "wt_1", RepoID: "rep_1", TaskID: "tsk_1", Path: r.dir}, "base")
	r.store.SetTrusted("rep_1", true)
	r.store.SetReview("tsk_1", "turn_1", 1)
	r.probe.Changes[r.dir] = true
	r.svc = r.newService()
	return r
}

// newService builds a service on the rig's store and runner: calling it again
// is a restart of ygg.
func (r *rig) newService() *verify.Service {
	r.t.Helper()
	svc, err := verify.New(verify.Deps{
		Store: r.store, Builds: r.runner, Logs: r.runner, Messenger: r.msg, Probe: r.probe,
		Toolchains: []builds.Toolchain{tcRef{r.tc}}, Hub: r.hub, Redactor: safety.NewRedactor(secret),
		OpenFS: func(string) fs.FS { return r.files },
		Clock: func() time.Time {
			return time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(r.n.Add(1)) * time.Second)
		},
		IDs: func(prefix string) string { return fmt.Sprintf("%s_%d", prefix, r.n.Add(1)) },
		Log: slog.New(slog.NewTextHandler(testWriter{r.t}, &slog.HandlerOptions{Level: slog.LevelWarn})),
		// no waiting between enqueue retries
		EnqueueBackoff: time.Nanosecond,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return svc
}

// tcRef lets the test change the toolchain's commands after the service exists.
type tcRef struct{ p *fakeTC }

func (t tcRef) Name() string                                { return t.p.Name() }
func (t tcRef) Detect(repo fs.FS) (builds.Detection, bool)  { return t.p.Detect(repo) }
func (t tcRef) Commands(d builds.Detection) builds.Commands { return t.p.Commands(d) }
func (t tcRef) Artifacts(d builds.Detection) []string       { return t.p.Artifacts(d) }
func (t tcRef) DepsFiles(d builds.Detection) []string       { return t.p.DepsFiles(d) }

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Logf("%s", p); return len(p), nil }

func (r *rig) reconcile() {
	r.t.Helper()
	if err := r.svc.Reconcile(context.Background()); err != nil {
		r.t.Fatalf("reconcile: %v", err)
	}
}

// settle reconciles until the stored state stops changing (at most 8 passes).
func (r *rig) settle() {
	r.t.Helper()
	prev := ""
	for i := 0; i < 8; i++ {
		r.reconcile()
		cur := fmt.Sprintf("%#v", r.states())
		if cur == prev {
			return
		}
		prev = cur
	}
}

func (r *rig) records() []verify.Record {
	r.t.Helper()
	recs, err := r.svc.List(context.Background(), "tsk_1")
	if err != nil {
		r.t.Fatal(err)
	}
	return recs
}

func (r *rig) states() []cv.State {
	var out []cv.State
	for _, rec := range r.records() {
		out = append(out, rec.State)
	}
	return out
}

// state is the state of the one repo of the rig.
func (r *rig) state() cv.State {
	r.t.Helper()
	recs := r.records()
	if len(recs) != 1 {
		r.t.Fatalf("%d verifications, want 1", len(recs))
	}
	return recs[0].State
}

func (r *rig) auditActions() []string {
	var out []string
	for _, e := range r.store.Audit() {
		out = append(out, e.Action)
	}
	return out
}
